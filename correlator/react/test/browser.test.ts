// The whole path: a built site, Playwright with the fixture, then observe.
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  cpSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { before, test } from "node:test";
import { buildCommand } from "../build.ts";
import { run } from "../src/cli.ts";
import { CannotTell, observe } from "../src/observe.ts";
import { partsOf } from "../src/record.ts";
import { buildSite } from "./e2e/build-site.ts";
import { closePart } from "./e2e/core-parts.ts";

const root = join(import.meta.dirname, "..");
const out = mkdtempSync(join(tmpdir(), "react-correlator-run-"));
const config = {
  bundles: [
    {
      dist: "testdata/site/dist",
      html: "testdata/site/index.html",
      public: "testdata/site/public",
    },
  ],
};
// Never written, as for a batch whose setup recorded nothing.
const noSetup = join(out, "no-setup");
const setupDir = join(out, "_setup", "react");
const sliceDir = (slice: string) => join(out, slice, "react");
const dirs = (slice: string) => [noSetup, sliceDir(slice)];

before(async () => {
  await buildSite(join(root, "testdata/site"));
  const r = spawnSync("npx", ["playwright", "test", "-c", "test/e2e/playwright.config.ts"], {
    cwd: root,
    env: { ...process.env, COVERAGE_OUT: out },
    encoding: "utf8",
  });
  assert.equal(r.status, 0, r.stdout + r.stderr);
});

test("each spec file binds the browser functions it ran, and no others", () => {
  const shop = observe(root, dirs("test/e2e/shop.spec.ts"), config).touched;
  const about = observe(root, dirs("test/e2e/about.spec.ts"), config).touched;
  assert.ok(shop.includes("ts:testdata/site/src/cart.ts#fn:addToCart"));
  assert.ok(!about.includes("ts:testdata/site/src/cart.ts#fn:addToCart"));
  assert.ok(about.includes("ts:testdata/site/src/pages/about.ts#fn:showAbout"));
  assert.ok(!shop.includes("ts:testdata/site/src/pages/about.ts#fn:showAbout"));
  for (const t of [shop, about]) {
    assert.ok(!t.includes("ts:testdata/site/src/cart.ts#fn:neverCalled"));
    assert.ok(t.includes("ts:testdata/site/src/main.ts#init"));
    assert.ok(t.includes("ts:testdata/site/src/cart.ts#const:PRICES"));
    assert.ok(t.includes("file:testdata/site/index.html"));
  }
});

test("the builds are found from the bundler config in the tree", () => {
  const found = observe(root, dirs("test/e2e/shop.spec.ts")).touched;
  assert.deepEqual(found, observe(root, dirs("test/e2e/shop.spec.ts"), config).touched);
});

test("with claims, the builds are found from the claimed bundler configs alone", () => {
  const shop = dirs("test/e2e/shop.spec.ts");
  const claimed = ["testdata/site/index.html", "testdata/site/vite.config.ts"];
  assert.deepEqual(
    observe(root, shop, {}, undefined, claimed).touched,
    observe(root, shop, config).touched,
  );
  assert.throws(
    () => observe(root, shop, {}, undefined, ["testdata/site/index.html"]),
    /no build in the tree holds it \(builds: none found\)/,
  );
});

test("a script on the app's origin that no build holds can't be named", () => {
  const dir = mkdtempSync(join(tmpdir(), "react-correlator-unknown-"));
  const rec = JSON.parse(
    readFileSync(join(sliceDir("test/e2e/shop.spec.ts"), "1", "browser.json"), "utf8"),
  );
  assert.deepEqual(rec.documents, ["http://site.test/"]);
  rec.scripts.push({
    url: "http://site.test/assets/elsewhere.js",
    sha: "",
    ran: [[0, 10]],
    fns: [0],
  });
  rec.scripts.push({ url: "https://widgets.example/w.js", sha: "", ran: [[0, 10]], fns: [0] });
  writeFileSync(join(dir, "browser.json"), JSON.stringify(rec));
  writeFileSync(join(dir, "done"), "");
  assert.throws(
    () => observe(root, [noSetup, dir], config),
    /elsewhere\.js ran on the app's origin, but no build in the tree holds it/,
  );
  rec.scripts.splice(-2, 1);
  writeFileSync(join(dir, "browser.json"), JSON.stringify(rec));
  assert.doesNotThrow(() => observe(root, [noSetup, dir], config));
});

test("what ran only while modules loaded is marked as setup-like", () => {
  const { setup_like } = observe(root, dirs("test/e2e/shop.spec.ts"), config);
  assert.ok(setup_like.includes("ts:testdata/site/src/cart.ts#const:PRICES"));
  // A click handler registered by the module's top-level code ran after loading, so this init is
  // the slice's own.
  assert.ok(!setup_like.includes("ts:testdata/site/src/main.ts#init"));
  assert.ok(!setup_like.includes("ts:testdata/site/src/cart.ts#fn:addToCart"));
});

test("the Node side binds the helpers each spec file ran, per function", () => {
  const shop = observe(root, dirs("test/e2e/shop.spec.ts"), config).touched;
  assert.ok(shop.includes("ts:test/e2e/helpers.ts#fn:open"));
  assert.ok(!shop.includes("ts:test/e2e/helpers.ts#fn:neverUsedHelper"));
});

test("the first directory is the setup's, whatever its name: what ran only there is setup-like", () => {
  const asSetup = join(sliceDir("test/e2e/about.spec.ts"), "1");
  const both = observe(root, [asSetup, sliceDir("test/e2e/shop.spec.ts")], config);
  const alone = observe(root, dirs("test/e2e/shop.spec.ts"), config);
  const setupOnly = observe(root, [asSetup], config).touched.filter(
    (u) => !alone.touched.includes(u),
  );
  assert.ok(setupOnly.includes("ts:testdata/site/src/pages/about.ts#fn:showAbout"));
  for (const u of setupOnly) assert.ok(both.setup_like.includes(u), u);
  assert.ok(both.touched.includes("ts:testdata/site/src/pages/about.ts#fn:showAbout"));
});

test("what ran before the first test is kept apart", () => {
  const setup = observe(root, [setupDir], config).touched;
  assert.ok(setup.every((u) => !u.startsWith("ts:testdata/")));
});

test("collect succeeds, and refuses an unfinished slice when the suite runs a stack", () => {
  const done = run(
    "collect",
    JSON.stringify({
      protocol: 1,
      action: "slice-end",
      dir: sliceDir("test/e2e/shop.spec.ts"),
      stack: true,
    }),
  );
  assert.equal(done.code, 0);
  const nowhere = join(out, "nowhere");
  assert.equal(
    run("collect", JSON.stringify({ protocol: 1, action: "slice-end", dir: nowhere })).code,
    0,
  );
  const missing = run(
    "collect",
    JSON.stringify({ protocol: 1, action: "slice-end", dir: nowhere, stack: true }),
  );
  assert.equal(missing.code, 1);
});

test("observe can't tell when the build is not what ran, and fails without data", () => {
  const dir = mkdtempSync(join(tmpdir(), "react-correlator-forged-"));
  const rec = JSON.parse(
    readFileSync(join(sliceDir("test/e2e/shop.spec.ts"), "1", "browser.json"), "utf8"),
  );
  for (const s of rec.scripts) s.sha = "0000000000000000";
  writeFileSync(join(dir, "browser.json"), JSON.stringify(rec));
  writeFileSync(join(dir, "done"), "");
  const r = run(
    "observe",
    JSON.stringify({ protocol: 1, tree: root, dirs: [noSetup, dir], config }),
  );
  assert.equal(r.code, 3);
  assert.match(r.err ?? "", /differs from the script the browser ran/);
  const gone = run(
    "observe",
    JSON.stringify({ protocol: 1, tree: root, dirs: [noSetup, join(out, "nowhere")], config }),
  );
  assert.equal(gone.code, 3, "a missing recording can't tell");
  const empty = mkdtempSync(join(tmpdir(), "react-correlator-empty-"));
  assert.throws(() => observe(root, [noSetup, empty], config), /is empty/);
  writeFileSync(join(empty, "node.json"), JSON.stringify({ version: 1, scripts: [] }));
  assert.throws(() => observe(root, [noSetup, empty], config), /unfinished recording/);
  writeFileSync(join(empty, "done"), "");
  assert.deepEqual(observe(root, [noSetup, empty], config), {
    touched: [],
    setup_like: [],
    missing: [],
    generated: [],
    whole: [],
    served: [],
    foreign: [],
  });
  writeFileSync(join(empty, "browser.json"), "{not json");
  assert.throws(() => observe(root, [noSetup, empty], config), SyntaxError);
  assert.throws(
    () => observe(root, [noSetup, empty], config),
    (e: Error) =>
      !(e instanceof CannotTell) &&
      /browser\.json \(9 bytes\) is not JSON \(.+\): the slice's recording is unusable$/.test(
        e.message,
      ),
  );
  writeFileSync(join(empty, "browser.json"), "{}");
  assert.throws(
    () => observe(root, [noSetup, empty], config),
    (e: Error) =>
      !(e instanceof CannotTell) &&
      /browser\.json \(2 bytes\) lists no scripts, so the fixture didn't write it whole: the slice's recording is unusable$/.test(
        e.message,
      ),
  );
});

test("a build made outside the tree is read from where the run put it", () => {
  const elsewhere = mkdtempSync(join(tmpdir(), "react-correlator-prepared-"));
  cpSync(join(root, "testdata/site/dist"), join(elsewhere, "site-out/testdata/site/dist"), {
    recursive: true,
  });
  const moved = {
    bundles: [{ dist: "testdata/site/moved-dist", html: "testdata/site/index.html" }],
  };
  cpSync(join(root, "testdata/site/dist"), join(elsewhere, "site-out/testdata/site/moved-dist"), {
    recursive: true,
  });
  const r = observe(root, dirs("test/e2e/shop.spec.ts"), {
    ...moved,
    build_root: join(elsewhere, "site-out"),
  }).touched;
  assert.ok(r.includes("ts:testdata/site/src/cart.ts#fn:addToCart"));
  assert.throws(
    () => observe(root, dirs("test/e2e/shop.spec.ts"), moved),
    /no build in the tree holds it \(builds: expected at testdata\/site\/moved-dist \(not found\)\)/,
  );
});

test("a script without a map is judged by the build where the run put it", () => {
  // The build only outside the tree, one chunk's map gone.
  const elsewhere = mkdtempSync(join(tmpdir(), "react-correlator-outside-"));
  const dist = join(elsewhere, "testdata/site/outside-dist");
  cpSync(join(root, "testdata/site/dist"), dist, { recursive: true });
  const chunk = readdirSync(join(dist, "assets")).find(
    (f) => f.startsWith("about-") && f.endsWith(".js"),
  );
  assert.ok(chunk);
  rmSync(join(dist, "assets", `${chunk}.map`));
  const outsideOnly = {
    bundles: [{ dist: "testdata/site/outside-dist", html: "testdata/site/index.html" }],
    build_root: elsewhere,
  };
  const r = observe(root, dirs("test/e2e/about.spec.ts"), outsideOnly);
  assert.ok(
    r.generated.includes(`testdata/site/outside-dist/assets/${chunk}`),
    r.generated.join(" "),
  );
  assert.ok(r.touched.includes("ts:testdata/site/src/main.ts#init"));
});

test("a script several builds hold alike goes to the build of the app its origin serves", () => {
  // Two apps served alike from /: the main script only in "web", the about chunk in "host" too,
  // without its map.
  const elsewhere = mkdtempSync(join(tmpdir(), "react-correlator-twoapps-"));
  const web = join(elsewhere, "testdata/site/web-dist");
  const host = join(elsewhere, "testdata/site/host-dist");
  cpSync(join(root, "testdata/site/dist"), web, { recursive: true });
  const chunk = readdirSync(join(web, "assets")).find(
    (f) => f.startsWith("about-") && f.endsWith(".js"),
  );
  assert.ok(chunk);
  mkdirSync(join(host, "assets"), { recursive: true });
  cpSync(join(web, "assets", chunk), join(host, "assets", chunk));
  const twoApps = {
    bundles: [
      { dist: "testdata/site/host-dist", html: "testdata/site/index.html" },
      { dist: "testdata/site/web-dist", html: "testdata/site/index.html" },
    ],
    build_root: elsewhere,
  };
  const o = observe(root, dirs("test/e2e/about.spec.ts"), twoApps);
  assert.ok(
    o.touched.includes("ts:testdata/site/src/pages/about.ts#fn:showAbout"),
    o.touched.join(" "),
  );
  assert.deepEqual(o.served, ["http://site.test serves testdata/site/web-dist"]);
  const c = run(
    "observe",
    JSON.stringify({
      protocol: 1,
      tree: root,
      dirs: dirs("test/e2e/about.spec.ts"),
      config: twoApps,
    }),
  );
  assert.equal(c.code, 0, c.err);
  assert.match(c.err ?? "", /origins: http:\/\/site\.test serves testdata\/site\/web-dist/);
});

test("a build_root the observing step wasn't given is named once, with what to do", () => {
  const r = run(
    "observe",
    JSON.stringify({
      protocol: 1,
      tree: root,
      dirs: dirs("test/e2e/shop.spec.ts"),
      config: { ...config, build_root: "prepared/tree" },
    }),
  );
  assert.equal(r.code, 3);
  assert.match(
    r.err ?? "",
    /^build_root prepared\/tree does not exist here \(.+\): the step that observes must be given the built files: see https:\/\/github.com\/sideways-works\/pipelinegen-correlator-react#where-observe-runs-and-what-it-needs-there$/,
  );
});

test("a build without source maps can't tell, naming the chunk and the fix", () => {
  const elsewhere = mkdtempSync(join(tmpdir(), "react-correlator-nomap-"));
  const dist = join(elsewhere, "testdata/site/nomap-dist");
  cpSync(join(root, "testdata/site/dist"), dist, {
    recursive: true,
    filter: (src) => !src.endsWith(".map"),
  });
  const nomap = {
    bundles: [
      { dist: "testdata/site/nomap-dist", html: "testdata/site/index.html" },
      { dist: "testdata/site/dist", html: "testdata/site/index.html" },
    ],
    build_root: elsewhere,
  };
  assert.throws(
    () => observe(root, dirs("test/e2e/shop.spec.ts"), nomap),
    /the app's build has no source map for testdata\/site\/nomap-dist\/assets\/[^:]+\.js: build with source maps for recording runs/,
  );
});

const phone = "ts:testdata/site/src/layout.ts#fn:phoneLayout";
const desktop = "ts:testdata/site/src/layout.ts#fn:desktopLayout";

test("a spec file run once per project leaves one part per run, and observe adds them up", () => {
  const shop = sliceDir("test/e2e/shop.spec.ts");
  const parts = partsOf(shop);
  assert.deepEqual(
    parts.map((p) => p.slice(shop.length + 1)),
    ["1", "2"],
  );
  assert.equal(partsOf(setupDir).length, 2, "the setup runs once per project too");
  const each = parts.map((p) => observe(root, [noSetup, p], config).touched);
  assert.deepEqual(each.map((t) => [t.includes(phone), t.includes(desktop)]).sort(), [
    [false, true],
    [true, false],
  ]);
  const all = observe(root, dirs("test/e2e/shop.spec.ts"), config).touched;
  assert.ok(all.includes(phone) && all.includes(desktop));
  assert.deepEqual(all, [...new Set([...(each[0] ?? []), ...(each[1] ?? [])])].sort());
});

test("a slice with a part that never finished can't tell", () => {
  const copy = join(mkdtempSync(join(tmpdir(), "react-correlator-cut-")), "react");
  cpSync(sliceDir("test/e2e/shop.spec.ts"), copy, { recursive: true });
  rmSync(join(copy, "2", "done"));
  assert.throws(
    () => observe(root, [noSetup, copy], config),
    (e: unknown) => e instanceof CannotTell && /2 holds an unfinished recording/.test(e.message),
  );
  const r = run(
    "observe",
    JSON.stringify({ protocol: 1, tree: root, dirs: [noSetup, copy], config }),
  );
  assert.equal(r.code, 3);
  const c = run(
    "collect",
    JSON.stringify({ protocol: 1, action: "slice-end", dir: copy, stack: true }),
  );
  assert.equal(c.code, 1);
  assert.match(c.err ?? "", /a part without its done file/);
});

test("the built fixture and the built command agree on what a slice's directory holds", async () => {
  // The run above used dist/playwright.mjs, the fixture a release carries.
  const command = await buildCommand(mkdtempSync(join(tmpdir(), "react-correlator-built-")));
  const req = JSON.stringify({
    protocol: 1,
    tree: root,
    dirs: dirs("test/e2e/shop.spec.ts"),
    config,
  });
  const built = spawnSync(process.execPath, [command, "observe"], {
    cwd: root,
    input: req,
    encoding: "utf8",
  });
  assert.equal(built.status, 0, built.stderr);
  const answer = JSON.parse(built.stdout) as { touched: string[] };
  assert.ok(answer.touched.includes(phone) && answer.touched.includes(desktop));
  assert.deepEqual(answer, run("observe", req).out);
});

test("the recording keeps each served script and its map, so observe needs no build", () => {
  const kept = readdirSync(join(setupDir, "scripts"));
  assert.ok(
    kept.some((f) => f.endsWith(".js")) && kept.some((f) => f.endsWith(".map")),
    kept.join(" "),
  );
  const js = kept.filter((f) => f.endsWith(".js"));
  assert.equal(new Set(js).size, js.length);
  const want = observe(root, [setupDir, sliceDir("test/e2e/shop.spec.ts")], config).touched;
  const noBuild = {
    bundles: [
      {
        dist: "testdata/site/gone-dist",
        html: "testdata/site/index.html",
        public: "testdata/site/public",
      },
    ],
  };
  const got = observe(root, [setupDir, sliceDir("test/e2e/shop.spec.ts")], noBuild).touched;
  assert.deepEqual(got, want);
  assert.ok(got.includes("ts:testdata/site/src/cart.ts#fn:addToCart"));
  const r = run(
    "observe",
    JSON.stringify({
      protocol: 1,
      tree: root,
      dirs: [setupDir, sliceDir("test/e2e/shop.spec.ts")],
      config: { ...noBuild, build_root: "prepared/tree" },
    }),
  );
  assert.equal(r.code, 0, r.err);
});

test("a spec file run on two projects is two parts, each in the directory the core names", () => {
  const twice = mkdtempSync(join(tmpdir(), "react-correlator-parts-"));
  const state = join(twice, ".core.json");
  const r = spawnSync("npx", ["playwright", "test", "-c", "test/e2e/playwright.config.ts"], {
    cwd: root,
    env: { ...process.env, COVERAGE_OUT: twice, CORE_PARTS: state },
    encoding: "utf8",
  });
  assert.equal(r.status, 0, r.stdout + r.stderr);
  closePart(state);
  const slice = "test/e2e/shop.spec.ts";
  const first = join(twice, slice, "react");
  const second = join(twice, ".pipelinegen", "part-2", slice, "react");
  assert.equal(partsOf(first).length, 1);
  assert.equal(partsOf(second).length, 1);
  const setup = join(twice, "_setup", "react");
  const phone = observe(root, [setup, first], config).touched;
  const desktop = observe(root, [setup, second], config).touched;
  assert.ok(phone.includes("ts:testdata/site/src/layout.ts#fn:phoneLayout"));
  assert.ok(!phone.includes("ts:testdata/site/src/layout.ts#fn:desktopLayout"));
  assert.ok(desktop.includes("ts:testdata/site/src/layout.ts#fn:desktopLayout"));
  const both = run(
    "observe",
    JSON.stringify({ protocol: 1, tree: root, dirs: [setup, first, second], config }),
  );
  assert.equal(both.code, 0, both.err);
  const touched = (both.out as { touched: string[] }).touched;
  assert.deepEqual(touched, [...new Set([...phone, ...desktop])].sort());
  // The fixture keeps each script once per worker, so its saving code runs in whichever spec file
  // first meets a script, which two Playwright runs need not share.
  const ofTheSite = (id: string) =>
    !id.startsWith("ts:test/e2e/core-parts.ts") && !id.startsWith("ts:dist/playwright.mjs#");
  assert.deepEqual(
    touched.filter(ofTheSite),
    observe(root, [setupDir, sliceDir(slice)], config).touched.filter(ofTheSite),
  );
});

test("a kept script without a map is the bundler's only where its own app served maps", () => {
  const base = mkdtempSync(join(tmpdir(), "react-correlator-origins-"));
  const setup = join(base, "_setup", "react");
  cpSync(join(setupDir, "scripts"), join(setup, "scripts"), { recursive: true });
  const text = "var bundlerOwn=1;\n";
  const sha = createHash("sha256").update(text).digest("hex").slice(0, 16);
  writeFileSync(join(setup, "scripts", `${sha}.js`), text);
  writeFileSync(join(setup, "scripts", `${sha}.nomap`), "");
  const recorded = JSON.parse(
    readFileSync(
      join(partsOf(sliceDir("test/e2e/shop.spec.ts"))[0] as string, "browser.json"),
      "utf8",
    ),
  ) as { scripts: { url: string }[] };
  const noBuild = {
    bundles: [{ dist: "testdata/site/gone-dist", html: "testdata/site/index.html" }],
  };
  const slice = (origin: string) => {
    const part = join(base, origin, "react", "1");
    mkdirSync(part, { recursive: true });
    writeFileSync(
      join(part, "browser.json"),
      JSON.stringify({
        ...recorded,
        version: 1,
        browser: "chromium",
        supported: true,
        scripts: [
          ...recorded.scripts,
          { url: `http://${origin}/assets/runtime-X.js`, sha, ran: [[0, text.length]], fns: [0] },
        ],
      }),
    );
    writeFileSync(join(part, "done"), "");
    return join(base, origin, "react");
  };
  const same = observe(root, [setup, slice("site.test")], noBuild);
  assert.ok(
    same.generated.includes("testdata/site/gone-dist/assets/runtime-X.js"),
    same.generated.join(" "),
  );
  assert.throws(
    () => observe(root, [setup, slice("127.0.0.1:7152")], noBuild),
    /the app's build has no source map for testdata\/site\/gone-dist\/assets\/runtime-X\.js/,
  );
  assert.deepEqual(observe(root, [setup, slice("other.test")], noBuild).foreign, [
    "http://other.test (1)",
  ]);
});

test("a module's init counts when the module was evaluated, not only when its code ran", () => {
  const shop = observe(root, dirs("test/e2e/shop.spec.ts"), config).touched;
  assert.ok(shop.includes("ts:testdata/site/src/registry.ts#init"));
  assert.ok(shop.includes("ts:testdata/site/src/registry.ts#const:TABLE"));
  assert.ok(!shop.includes("ts:testdata/site/src/registry.ts#fn:lookup"));
  assert.ok(shop.includes("ts:testdata/site/src/format.ts#init"));
  assert.ok(!shop.includes("ts:testdata/site/src/format.ts#fn:fmt"));
  assert.ok(!shop.includes("ts:testdata/site/src/pages/about-words.ts#init"));
  const about = observe(root, dirs("test/e2e/about.spec.ts"), config).touched;
  assert.ok(about.includes("ts:testdata/site/src/pages/about-words.ts#init"));
  assert.ok(about.includes("ts:testdata/site/src/pages/about-words.ts#fn:aboutWords"));
});

test("a third party's page the test opened is foreign code, not a build observe lacks", () => {
  const base = mkdtempSync(join(tmpdir(), "react-correlator-foreign-"));
  const recorded = JSON.parse(
    readFileSync(
      join(partsOf(sliceDir("test/e2e/shop.spec.ts"))[0] as string, "browser.json"),
      "utf8",
    ),
  ) as { scripts: { url: string }[]; documents?: string[] };
  const slice = (name: string, origin: string) => {
    const part = join(base, name, "react", "1");
    mkdirSync(part, { recursive: true });
    writeFileSync(
      join(part, "browser.json"),
      JSON.stringify({
        ...recorded,
        documents: [...(recorded.documents ?? []), `${origin}/onboarding`],
        scripts: [
          ...recorded.scripts,
          {
            url: `${origin}/accessoryLayer-810bab5b.js`,
            sha: "0123456789abcdef",
            ran: [[0, 10]],
            fns: [0],
          },
        ],
      }),
    );
    writeFileSync(join(part, "done"), "");
    return join(base, name, "react");
  };
  const plain = join(base, "plain", "react", "1");
  mkdirSync(plain, { recursive: true });
  writeFileSync(join(plain, "browser.json"), JSON.stringify(recorded));
  writeFileSync(join(plain, "done"), "");
  const own = observe(root, [noSetup, join(base, "plain", "react")], config).touched;
  const third = observe(root, [noSetup, slice("third", "https://connect-js.pay.example")], config);
  assert.deepEqual(third.touched, own);
  assert.deepEqual(third.foreign, ["https://connect-js.pay.example (1)"]);
  const c = run(
    "observe",
    JSON.stringify({
      protocol: 1,
      tree: root,
      dirs: [noSetup, slice("cli", "https://connect-js.pay.example")],
      config,
    }),
  );
  assert.equal(c.code, 0, c.err);
  assert.match(
    c.err ?? "",
    /scripts of other origins ran and bind nothing.*connect-js\.pay\.example \(1\)/,
  );
  // A page on this machine or the app's own site is the app's: a script there that no build holds
  // is a build observe lacks.
  assert.throws(
    () => observe(root, [noSetup, slice("local", "http://127.0.0.1:7153")], config),
    /ran on the app's origin, but no build in the tree holds it/,
  );
});

test("a build whose copy differs from what ran says whether the recording kept the script", () => {
  const base = mkdtempSync(join(tmpdir(), "react-correlator-differs-"));
  const sha = "fedcba9876543210";
  const part = join(base, "s", "react", "1");
  mkdirSync(part, { recursive: true });
  writeFileSync(
    join(part, "browser.json"),
    JSON.stringify({
      version: 1,
      browser: "chromium",
      supported: true,
      scripts: [{ url: "http://site.test/assets/main.js", sha, ran: [[0, 10]], fns: [0] }],
      requests: [],
      documents: ["http://site.test/"],
    }),
  );
  writeFileSync(join(part, "done"), "");
  const setup = join(base, "_setup", "react");
  assert.throws(
    () => observe(root, [setup, join(base, "s", "react")], config),
    /testdata\/site\/dist\/assets\/main\.js in the build differs from the script the browser ran \(http:\/\/site\.test\/assets\/main\.js\), and the recording kept no copy of it \(browser sha fedcba9876543210; file sha [0-9a-f]{16}, \d+ bytes\)/,
  );
  mkdirSync(join(setup, "scripts"), { recursive: true });
  const built = readFileSync(join(root, "testdata/site/dist/assets/main.js"), "utf8");
  writeFileSync(join(setup, "scripts", `${sha}.js`), `${built.slice(0, 7)}X${built.slice(8)}`);
  assert.throws(
    () => observe(root, [setup, join(base, "s", "react")], config),
    /first difference at byte 7\); the recording kept that script but not its source map: the fixture recorded no reason/,
  );
  writeFileSync(
    join(setup, "scripts", `${sha}.mapfail`),
    "http://site.test/assets/main.js.map: in the page: HTTP 404; from the test process: fetch failed\n",
  );
  assert.throws(
    () => observe(root, [setup, join(base, "s", "react")], config),
    /not its source map: http:\/\/site\.test\/assets\/main\.js\.map: in the page: HTTP 404; from the test process: fetch failed$/,
  );
  writeFileSync(
    join(part, "browser.json"),
    JSON.stringify({
      version: 1,
      browser: "chromium",
      supported: true,
      scripts: [
        {
          url: "http://site.test/assets/main.js",
          sha: createHash("sha256").update("").digest("hex").slice(0, 16),
          size: 0,
          ran: [[0, 10]],
          fns: [0],
        },
      ],
      requests: [],
      documents: ["http://site.test/"],
    }),
  );
  assert.throws(
    () => observe(root, [setup, join(base, "s", "react")], config),
    /testdata\/site\/dist\/assets\/main\.js in the build can't be checked against the script the browser ran \(http:\/\/site\.test\/assets\/main\.js\): the recording kept no copy of it: the browser's coverage came without the script's text$/,
  );
});

test("a map the fixture can't fetch is kept as a reason beside its script", () => {
  const out2 = mkdtempSync(join(tmpdir(), "react-correlator-nomaps-"));
  const r = spawnSync(
    "npx",
    ["playwright", "test", "-c", "test/e2e/playwright.config.ts", "test/e2e/shop.spec.ts"],
    {
      cwd: root,
      env: { ...process.env, COVERAGE_OUT: out2, MAPS_GONE: "1" },
      encoding: "utf8",
    },
  );
  assert.equal(r.status, 0, r.stdout + r.stderr);
  const kept = join(out2, "_setup", "react", "scripts");
  const names = readdirSync(kept);
  const fails = names.filter((n) => n.endsWith(".mapfail"));
  assert.ok(fails.length > 0, names.join(" "));
  assert.ok(!names.some((n) => n.endsWith(".map")), names.join(" "));
  for (const f of fails)
    assert.match(
      readFileSync(join(kept, f), "utf8"),
      /^http:\/\/site\.test\/assets\/[^ ]+\.js\.map: in the page: HTTP 404; from the test process: /,
    );
  const touched = observe(
    root,
    [join(out2, "_setup", "react"), join(out2, "test/e2e/shop.spec.ts", "react")],
    config,
  ).touched;
  assert.ok(touched.includes("ts:testdata/site/src/cart.ts#fn:addToCart"));
});

// Scripts of several origins in one recording, each kept as the fixture keeps it. The app's build
// is not where observe runs.
const goneBuild = {
  bundles: [
    {
      dist: "testdata/site/gone-dist",
      html: "testdata/site/index.html",
      public: "testdata/site/public",
    },
  ],
};
interface Added {
  url: string;
  text: string;
  map?: object;
}
function recordWith(
  name: string,
  added: Added[],
  opts: { documents?: string[]; appScripts?: boolean } = {},
): string[] {
  const base = mkdtempSync(join(tmpdir(), `react-correlator-${name}-`));
  const setup = join(base, "_setup", "react");
  mkdirSync(join(setup, "scripts"), { recursive: true });
  const recorded = JSON.parse(
    readFileSync(
      join(partsOf(sliceDir("test/e2e/shop.spec.ts"))[0] as string, "browser.json"),
      "utf8",
    ),
  ) as { scripts: { url: string }[]; documents?: string[] };
  const own = opts.appScripts === false ? [] : recorded.scripts;
  if (own.length) cpSync(join(setupDir, "scripts"), join(setup, "scripts"), { recursive: true });
  const scripts: object[] = [...own];
  for (const a of added) {
    const sha = createHash("sha256").update(a.text).digest("hex").slice(0, 16);
    writeFileSync(join(setup, "scripts", `${sha}.js`), a.text);
    if (a.map) writeFileSync(join(setup, "scripts", `${sha}.map`), JSON.stringify(a.map));
    else writeFileSync(join(setup, "scripts", `${sha}.nomap`), "");
    scripts.push({ url: a.url, sha, ran: [[0, a.text.length]], fns: [0] });
  }
  const part = join(base, "s", "react", "1");
  mkdirSync(part, { recursive: true });
  writeFileSync(
    join(part, "browser.json"),
    JSON.stringify({
      ...recorded,
      scripts,
      documents: [
        ...(opts.appScripts === false ? [] : (recorded.documents ?? [])),
        ...(opts.documents ?? []),
      ],
    }),
  );
  writeFileSync(join(part, "done"), "");
  return [setup, join(base, "s", "react")];
}

const vendor: Added = {
  url: "https://connect.vendor.example/v1.0/connect.js",
  text: "window.vendorConnect=function(){return 1};\n",
};
const vendorFrame: Added = {
  url: "https://js.vendor.example/endive/vendor.js",
  text: "window.vendorFrame=function(){return 2};\n",
};

test("a script of another origin at a path the build could hold is foreign, not the app's", () => {
  const own = observe(root, recordWith("own", []), goneBuild);
  const dirs3 = recordWith("vendor", [vendor, vendorFrame]);
  const r = observe(root, dirs3, goneBuild);
  assert.deepEqual(r.touched, own.touched);
  assert.deepEqual(r.generated, own.generated);
  assert.deepEqual(r.foreign, [
    "https://connect.vendor.example (1)",
    "https://js.vendor.example (1)",
  ]);
  const c = run(
    "observe",
    JSON.stringify({ protocol: 1, tree: root, dirs: dirs3, config: goneBuild }),
  );
  assert.equal(c.code, 0, c.err);
  assert.match(
    c.err ?? "",
    /2 scripts of other origins ran and bind nothing, as code no build of the tree holds: https:\/\/connect\.vendor\.example \(1\), https:\/\/js\.vendor\.example \(1\)/,
  );
  const built = observe(root, dirs3, config);
  assert.deepEqual(built.touched, observe(root, recordWith("own-built", []), config).touched);
  assert.deepEqual(built.generated, []);
  assert.deepEqual(built.foreign, [
    "https://connect.vendor.example (1)",
    "https://js.vendor.example (1)",
  ]);
});

test("a script without a map on any of the app's own origins still can't tell", () => {
  const noMap = /the app's build has no source map for testdata\/site\/gone-dist\//;
  const guest: Added = {
    url: "http://127.0.0.1:7152/assets/runtime-B.js",
    text: "var guestRuntime=1;\n",
  };
  assert.throws(() => observe(root, recordWith("port", [guest]), goneBuild), noMap);
  assert.throws(
    () =>
      observe(
        root,
        recordWith("port-page", [guest], { documents: ["http://127.0.0.1:7152/"] }),
        goneBuild,
      ),
    noMap,
  );
  const dev: Added = { url: "http://localhost:5173/@vite/client", text: "var devClient=1;\n" };
  assert.throws(
    () =>
      observe(root, recordWith("dev", [dev], { documents: ["http://localhost:5173/"] }), goneBuild),
    noMap,
  );
  const sibling: Added = { url: "http://cdn.site.test/assets/runtime-S.js", text: "var s=1;\n" };
  assert.throws(() => observe(root, recordWith("site", [sibling]), goneBuild), noMap);
  const cdn: Added = {
    url: "https://cdn.assets.example/static/assets/runtime-C.js",
    text: "var cdnRuntime=1;\n",
  };
  const withCdn = {
    bundles: [
      ...goneBuild.bundles,
      { dist: "testdata/site/cdn-dist", base: "https://cdn.assets.example/static/" },
    ],
  };
  assert.throws(() => observe(root, recordWith("cdn", [cdn]), withCdn), noMap);
  // Where nothing tells the app's origins apart, no script is taken for another origin's.
  const appOnly: Added = { url: "http://site.test/assets/app-Q.js", text: "var app=1;\n" };
  for (const added of [[appOnly], [vendor, appOnly], [vendor]])
    assert.throws(
      () =>
        observe(
          root,
          recordWith("nomaps", added, {
            appScripts: false,
            documents: ["http://site.test/"],
          }),
          goneBuild,
        ),
      noMap,
    );
});

test("an origin that served the app's script with its map is the app's, wherever it is", () => {
  // A map makes another site's host the app's, so a script there without one is the bundler's own.
  const recorded = JSON.parse(
    readFileSync(
      join(partsOf(sliceDir("test/e2e/shop.spec.ts"))[0] as string, "browser.json"),
      "utf8",
    ),
  ) as { scripts: { url: string; sha: string }[] };
  const main = recorded.scripts.find((s) => s.url.endsWith("/assets/main.js"));
  assert.ok(main);
  const text = readFileSync(join(setupDir, "scripts", `${main.sha}.js`), "utf8");
  const map = JSON.parse(readFileSync(join(setupDir, "scripts", `${main.sha}.map`), "utf8"));
  const runtime: Added = { url: "http://guest.test/assets/runtime-G.js", text: "var g=1;\n" };
  const r = observe(
    root,
    recordWith("guest", [{ url: "http://guest.test/assets/main.js", text, map }, runtime]),
    goneBuild,
  );
  assert.deepEqual(r.foreign, []);
  assert.ok(
    r.generated.includes("testdata/site/gone-dist/assets/runtime-G.js"),
    r.generated.join(" "),
  );
});

test("data:, blob: and extension scripts are read as before", () => {
  const own = observe(root, recordWith("own-odd", []), goneBuild);
  const data = observe(
    root,
    recordWith("data", [{ url: "data:text/javascript,var%20d%3D1", text: "var d=1" }]),
    goneBuild,
  );
  assert.deepEqual(data.touched, own.touched);
  assert.deepEqual(data.foreign, ["null (1)"]);
  const blob: Added = {
    url: "blob:http://site.test/6f1c2b9e-1d2a-4c3b-9e8f-0a1b2c3d4e5f",
    text: "var b=1;\n",
  };
  assert.throws(
    () => observe(root, recordWith("blob", [blob]), goneBuild),
    /blob:http:\/\/site\.test\/6f1c2b9e-1d2a-4c3b-9e8f-0a1b2c3d4e5f ran on the app's origin, but no build in the tree holds it/,
  );
  const extension: Added = {
    url: "chrome-extension://abcdefghijklmnop/content.js",
    text: "var e=1;\n",
  };
  assert.throws(
    () => observe(root, recordWith("extension", [extension]), goneBuild),
    /the app's build has no source map for testdata\/site\/gone-dist\/content\.js/,
  );
});

test("the inline script of a page on a deep route binds the page the build started from", () => {
  // Its inline script is recorded at the page's own URL, which names no file of the build.
  const inline: Added = {
    url: "http://site.test/account/settings",
    text: 'document.documentElement.dataset.ready = "1";',
  };
  const deep = recordWith("deep", [inline], { documents: ["http://site.test/account/settings"] });
  const part = join(deep[1] as string, "1", "browser.json");
  const rec = JSON.parse(readFileSync(part, "utf8")) as {
    scripts: { url: string }[];
    documents: string[];
  };
  rec.scripts = rec.scripts.filter((s) => s.url !== "http://site.test/");
  rec.documents = rec.documents.filter((d) => d !== "http://site.test/");
  writeFileSync(part, JSON.stringify(rec));
  for (const c of [goneBuild, config]) {
    const r = observe(root, deep, c);
    assert.ok(r.touched.includes("file:testdata/site/index.html"), r.touched.join(" "));
    assert.deepEqual(r.generated, []);
    assert.deepEqual(r.foreign, []);
  }
});

test("a page the build holds as a file of its own is still read from the build", () => {
  const elsewhere = mkdtempSync(join(tmpdir(), "react-correlator-pages-"));
  const dist = join(elsewhere, "testdata/site/pages-dist");
  cpSync(join(root, "testdata/site/dist"), dist, { recursive: true });
  writeFileSync(join(dist, "about.html"), "<!doctype html><script>var about=1;</script>\n");
  const part = join(elsewhere, "s", "react", "1");
  mkdirSync(part, { recursive: true });
  writeFileSync(
    join(part, "browser.json"),
    JSON.stringify({
      version: 1,
      browser: "chromium",
      supported: true,
      scripts: [
        { url: "http://site.test/about.html", sha: "0123456789abcdef", ran: [[0, 12]], fns: [0] },
      ],
      requests: ["http://site.test/about.html"],
      documents: ["http://site.test/about.html"],
    }),
  );
  writeFileSync(join(part, "done"), "");
  assert.throws(
    () =>
      observe(root, [join(elsewhere, "_setup", "react"), join(elsewhere, "s", "react")], {
        bundles: [{ dist: "testdata/site/pages-dist", html: "testdata/site/index.html" }],
        build_root: elsewhere,
      }),
    /testdata\/site\/pages-dist\/about\.html in the build differs from the script the browser ran/,
  );
});

test("a script no build holds says why the recording kept no copy of it", () => {
  const sha = (text: string) => createHash("sha256").update(text).digest("hex").slice(0, 16);
  const own = recordWith("unkept", []);
  const setup = own[0] as string;
  const part = join(own[1] as string, "1", "browser.json");
  const rec = JSON.parse(readFileSync(part, "utf8")) as { scripts: object[] };
  const scripts = [...rec.scripts];
  const lost = (s: object) => {
    rec.scripts = [...scripts, s];
    writeFileSync(part, JSON.stringify(rec));
    return () => observe(root, own, goneBuild);
  };
  const url = "http://site.test/assets/lazy-L.js";
  const text = "var lazy=1;\n";
  writeFileSync(join(setup, "scripts", `${sha(text)}.js`), text);
  writeFileSync(
    join(setup, "scripts", `${sha(text)}.mapfail`),
    `${url}.map: in the page: HTTP 404; from the test process: fetch failed`,
  );
  assert.throws(
    lost({ url, sha: sha(text), size: 12, ran: [[0, 12]], fns: [0] }),
    /http:\/\/site\.test\/assets\/lazy-L\.js ran on the app's origin, but no build in the tree holds it \(builds: expected at testdata\/site\/gone-dist \(not found\)\); the recording kept it without its source map: http:\/\/site\.test\/assets\/lazy-L\.js\.map: in the page: HTTP 404; from the test process: fetch failed$/,
  );
  assert.throws(
    lost({ url, sha: sha(""), size: 0, ran: [[0, 12]], fns: [0] }),
    /lazy-L\.js ran on the app's origin, but no build in the tree holds it \(.*\); the recording kept no copy of it: the browser's coverage came without the script's text$/,
  );
  assert.throws(
    lost({ url, sha: "0123456789abcdef", size: 40, ran: [[0, 12]], fns: [0] }),
    /lazy-L\.js ran on the app's origin, but no build in the tree holds it \(.*\); the recording kept no copy of it \(browser sha 0123456789abcdef, 40 bytes\)$/,
  );
});

test("with two apps and no build, a page and a runtime are named in the app their origin serves", () => {
  const twoApps = {
    bundles: [
      { dist: "testdata/site-vite/gone-dist", html: "testdata/site-vite/index.html" },
      ...goneBuild.bundles,
    ],
  };
  const deep = recordWith(
    "two-apps",
    [
      { url: "http://site.test/assets/runtime-R.js", text: "var runtimeR=1;\n" },
      {
        url: "http://site.test/account/settings",
        text: 'document.documentElement.dataset.ready = "2";',
      },
    ],
    { documents: ["http://site.test/account/settings"] },
  );
  const r = observe(root, deep, twoApps);
  assert.ok(r.touched.includes("file:testdata/site/index.html"), r.touched.join(" "));
  assert.ok(!r.touched.includes("file:testdata/site-vite/index.html"), r.touched.join(" "));
  assert.deepEqual(r.generated, ["testdata/site/gone-dist/assets/runtime-R.js"]);
});

test("a page a beforeAll hook opened is recorded in every test that uses it", () => {
  const shared = mkdtempSync(join(tmpdir(), "react-correlator-shared-"));
  const r = spawnSync(
    "npx",
    ["playwright", "test", "-c", "test/shared-page/playwright.config.ts"],
    {
      cwd: root,
      env: { ...process.env, COVERAGE_OUT: shared },
      encoding: "utf8",
    },
  );
  assert.equal(r.status, 0, r.stdout + r.stderr);
  const touched = observe(
    root,
    [join(shared, "_setup", "react"), join(shared, "test/shared-page/shared.spec.ts", "react")],
    config,
  ).touched;
  assert.ok(
    touched.includes("ts:testdata/site/src/pages/about.ts#fn:showAbout"),
    touched.join(" "),
  );
  assert.ok(touched.includes("ts:testdata/site/src/main.ts#init"), touched.join(" "));
});

test("a page served where a source map should be is not kept as the map", () => {
  const out3 = mkdtempSync(join(tmpdir(), "react-correlator-htmlmaps-"));
  const r = spawnSync(
    "npx",
    ["playwright", "test", "-c", "test/e2e/playwright.config.ts", "test/e2e/shop.spec.ts"],
    { cwd: root, env: { ...process.env, COVERAGE_OUT: out3, MAPS_HTML: "1" }, encoding: "utf8" },
  );
  assert.equal(r.status, 0, r.stdout + r.stderr);
  const kept = join(out3, "_setup", "react", "scripts");
  const names = readdirSync(kept);
  assert.ok(!names.some((n) => n.endsWith(".map")), names.join(" "));
  const fails = names.filter((n) => n.endsWith(".mapfail"));
  assert.ok(fails.length > 0, names.join(" "));
  for (const f of fails)
    assert.match(
      readFileSync(join(kept, f), "utf8"),
      /^http:\/\/site\.test\/assets\/[^ ]+\.js\.map: in the page: HTTP 200, text\/html: it is an HTML document, not a source map; from the test process: /,
    );
  const dirs3 = [join(out3, "_setup", "react"), join(out3, "test/e2e/shop.spec.ts", "react")];
  assert.throws(
    () => observe(root, dirs3, goneBuild),
    (e: Error) =>
      e instanceof CannotTell &&
      /\.js ran on the app's origin, but no build in the tree holds it \(.*\); the recording kept it without its source map: http:\/\/site\.test\/assets\/[^ ]+\.js\.map: in the page: HTTP 200, text\/html: it is an HTML document, not a source map/.test(
        e.message,
      ),
  );
  const touched = observe(root, dirs3, config).touched;
  assert.ok(touched.includes("ts:testdata/site/src/cart.ts#fn:addToCart"));
});

test("a kept map that is no source map can't tell for its script, and fails nothing", () => {
  const dirs4 = recordWith("badmap", []);
  const store = join(dirs4[0] as string, "scripts");
  const maps = readdirSync(store).filter((n) => n.endsWith(".map"));
  assert.ok(maps.length > 0);
  // What a fixture kept from a server that answered the map's URL with its page.
  for (const m of maps)
    writeFileSync(join(store, m), "<!doctype html>\n<html><body></body></html>\n");
  assert.throws(
    () => observe(root, dirs4, goneBuild),
    (e: Error) =>
      e instanceof CannotTell &&
      /\.js ran on the app's origin, but no build in the tree holds it \(.*\); the recording kept it without its source map: the map it names, http:\/\/site\.test\/assets\/[^ ]+\.js\.map, which the recording kept, is no source map: it is an HTML document$/.test(
        e.message,
      ),
  );
  assert.ok(
    observe(root, dirs4, config).touched.includes("ts:testdata/site/src/cart.ts#fn:addToCart"),
  );
  for (const m of maps) writeFileSync(join(store, m), '{"version":3,"mappings":"AAAA');
  assert.throws(
    () => observe(root, dirs4, goneBuild),
    (e: Error) =>
      e instanceof CannotTell &&
      /its source map \(the one the recording kept\) can't be read: /.test(e.message),
  );
});
