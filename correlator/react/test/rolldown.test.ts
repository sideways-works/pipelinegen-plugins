// The whole path again, built by Vite and rolldown with Vite's defaults, once code-split and once
// as one script (rolldown then starts dynamically imported modules lazily, in wrappers of its own).
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { cpSync, existsSync, mkdirSync, mkdtempSync, readdirSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { before, test } from "node:test";
import { observe } from "../src/observe.ts";
import { buildSiteVite } from "./e2e/build-site.ts";

const root = join(import.meta.dirname, "..");
const site = join(root, "testdata/site-vite");
const id = (unit: string) => `ts:testdata/site/src/${unit}`;

interface Variant {
  name: string;
  out: string;
  config: { bundles: { dist: string; html: string; public: string }[] };
}
const variants: Variant[] = [];

before(async () => {
  for (const split of [true, false]) {
    const dist = await buildSiteVite(site, split);
    const out = mkdtempSync(join(tmpdir(), "react-correlator-rolldown-"));
    const r = spawnSync("npx", ["playwright", "test", "-c", "test/e2e/playwright.config.ts"], {
      cwd: root,
      env: { ...process.env, COVERAGE_OUT: out, SITE_DIST: dist },
      encoding: "utf8",
    });
    assert.equal(r.status, 0, r.stdout + r.stderr);
    variants.push({
      name: split ? "split" : "whole",
      out,
      config: {
        bundles: [
          {
            dist: `testdata/site-vite/${split ? "dist-split" : "dist-whole"}`,
            html: "testdata/site-vite/index.html",
            public: "testdata/site/public",
          },
        ],
      },
    });
  }
});

const sliceOf = (v: Variant, spec: string) => [
  join(v.out, "no-setup"),
  join(v.out, "test/e2e", spec, "react"),
];

test("functions count by what ran, in either rolldown build", () => {
  for (const v of variants) {
    const shop = observe(root, sliceOf(v, "shop.spec.ts"), v.config).touched;
    const about = observe(root, sliceOf(v, "about.spec.ts"), v.config).touched;
    const say = `${v.name}: ${shop.join(" ")}`;
    assert.ok(shop.includes(id("cart.ts#fn:addToCart")), say);
    assert.ok(!shop.includes(id("cart.ts#fn:neverCalled")), say);
    assert.ok(!shop.includes(id("pages/about.ts#fn:showAbout")), say);
    assert.ok(!shop.includes(id("registry.ts#fn:lookup")), say);
    assert.ok(!shop.includes(id("format.ts#fn:fmt")), say);
    assert.ok(about.includes(id("pages/about.ts#fn:showAbout")), `${v.name}: ${about.join(" ")}`);
    assert.ok(about.includes(id("pages/about-words.ts#fn:aboutWords")), v.name);
    assert.ok(!about.includes(id("cart.ts#fn:addToCart")), v.name);
    assert.ok(shop.includes("file:testdata/site-vite/index.html"), say);
  }
});

test("inits count by evaluation, in either rolldown build", () => {
  for (const v of variants) {
    const shop = observe(root, sliceOf(v, "shop.spec.ts"), v.config).touched;
    const about = observe(root, sliceOf(v, "about.spec.ts"), v.config).touched;
    const say = `${v.name}: ${shop.join(" ")}`;
    for (const u of [
      "main.ts#init",
      "cart.ts#const:PRICES",
      "registry.ts#init",
      "registry.ts#const:TABLE",
      "format.ts#init",
    ])
      assert.ok(shop.includes(id(u)), `${u} in ${say}`);
    // A chunk never loaded, or rolldown's lazy wrapper never called.
    assert.ok(!shop.includes(id("pages/about-words.ts#init")), say);
    assert.ok(about.includes(id("pages/about-words.ts#init")), `${v.name}: ${about.join(" ")}`);
  }
});

test("both projects' runs add up: each layout counts", () => {
  for (const v of variants) {
    const shop = observe(root, sliceOf(v, "shop.spec.ts"), v.config).touched;
    assert.ok(shop.includes(id("layout.ts#fn:phoneLayout")), v.name);
    assert.ok(shop.includes(id("layout.ts#fn:desktopLayout")), v.name);
  }
});

test("rolldown's runtime chunk, without a map, is the bundler's own code", () => {
  const v = variants.find((x) => x.name === "split") as Variant;
  const dist = v.config.bundles[0]?.dist as string;
  const runtime = readdirSync(join(root, dist, "assets")).find((f) =>
    f.startsWith("rolldown-runtime-"),
  );
  assert.ok(runtime, "the split build has rolldown's runtime chunk");
  assert.ok(!existsSync(join(root, dist, "assets", `${runtime}.map`)), "and no map for it");
  const about = observe(root, sliceOf(v, "about.spec.ts"), v.config);
  assert.ok(about.generated.includes(`${dist}/assets/${runtime}`), about.generated.join(" "));
  assert.ok(about.touched.includes(id("pages/about.ts#fn:showAbout")));
});

test("the runtime chunk two apps' builds hold alike goes to the build its origin serves", () => {
  const v = variants.find((x) => x.name === "split") as Variant;
  const dist = v.config.bundles[0]?.dist as string;
  const runtime = readdirSync(join(root, dist, "assets")).find((f) =>
    f.startsWith("rolldown-runtime-"),
  ) as string;
  // Another app, listed first, whose build holds the same runtime and nothing with a map.
  const other = mkdtempSync(join(tmpdir(), "react-correlator-other-"));
  mkdirSync(join(other, "testdata/site-vite/other-dist/assets"), { recursive: true });
  cpSync(
    join(root, dist, "assets", runtime),
    join(other, "testdata/site-vite/other-dist/assets", runtime),
  );
  const twoApps = {
    bundles: [
      { dist: "testdata/site-vite/other-dist", html: "testdata/site-vite/index.html" },
      ...v.config.bundles,
    ],
    build_root: other,
  };
  const about = observe(root, sliceOf(v, "about.spec.ts"), twoApps);
  assert.deepEqual(about.served, [`http://site.test serves ${dist}`]);
  assert.ok(about.generated.includes(`${dist}/assets/${runtime}`), about.generated.join(" "));
});
