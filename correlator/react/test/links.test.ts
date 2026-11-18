import assert from "node:assert/strict";
import { join } from "node:path";
import { test } from "node:test";
import { run } from "../src/cli.ts";
import { Linker } from "../src/links.ts";
import { packageName, Resolver } from "../src/resolve.ts";
import { claimedIn, claimedPaths } from "./claimed.ts";

const tree = join(import.meta.dirname, "../testdata/tree");
const files = claimedPaths(tree);
const claimed = claimedIn(tree);
type L = { from: string; to: string; kind: string; narrowing?: string; while?: string };
const show = (ls: L[]) =>
  ls.map(
    (l) => `${l.from} -> ${l.to} ${l.kind}${l.narrowing ? ` (${l.narrowing} ${l.while})` : ""}`,
  );
const narrow = (ls: L[]) => ls.filter((l) => l.while !== "off");

test("package names drop the subpath", () => {
  assert.equal(packageName("react-dom/client"), "react-dom");
  assert.equal(packageName("@scope/pkg/deep/x"), "@scope/pkg");
});

test("specifiers resolve through relative paths, tsconfig paths, workspaces and aliases", () => {
  const files = [
    "package.json",
    "tsconfig.base.json",
    "apps/shop/tsconfig.json",
    "apps/shop/package.json",
    "apps/shop/src/app.tsx",
    "apps/shop/src/util/index.ts",
    "apps/shop/src/helper.ts",
    "packages/money/package.json",
    "packages/money/src/index.ts",
    "packages/money/src/rates.ts",
    "packages/money/src/format.ts",
  ];
  const r = new Resolver(tree, files, { aliases: { "~": "apps/shop/src" } });
  const from = "apps/shop/src/app.tsx";
  assert.deepEqual(r.resolve("./util", from), { file: "apps/shop/src/util/index.ts" });
  assert.deepEqual(r.resolve("./helper.js", from), { file: "apps/shop/src/helper.ts" });
  assert.deepEqual(r.resolve("./helper?raw", from), { file: "apps/shop/src/helper.ts" });
  assert.deepEqual(r.resolve("@money/format", from), { file: "packages/money/src/format.ts" });
  assert.deepEqual(r.resolve("@fixture/money", from), { file: "packages/money/src/index.ts" });
  assert.deepEqual(r.resolve("@fixture/money/rates", from), {
    file: "packages/money/src/rates.ts",
  });
  assert.deepEqual(r.resolve("~/helper", from), { file: "apps/shop/src/helper.ts" });
  assert.deepEqual(r.resolve("react-dom/client", from), { pkg: "react-dom" });
  assert.deepEqual(r.resolve("node:fs", from), { none: "node:fs" });
  assert.deepEqual(r.resolve("virtual:pwa", from), { none: "virtual:pwa" });
  assert.deepEqual(r.resolve("./gone", from), { missing: "./gone" });
});

test("narrow links: data, packages, stylesheets and plain files, never functions coverage sees", () => {
  const { links, cannot_tell } = new Linker(tree, files).links();
  assert.deepEqual(show(narrow(links)), [
    "ts:apps/shop/src/app.tsx#fn:App -> file:apps/shop/src/logo.svg reads",
    "ts:apps/shop/src/app.tsx#fn:App -> ts:apps/shop/src/app.tsx#const:TITLE reads",
    "ts:apps/shop/src/app.tsx#fn:Cart -> dep:npm:react uses",
    "ts:apps/shop/src/app.tsx#init -> dep:npm:react uses",
    "ts:apps/shop/src/app.tsx#init -> file:apps/shop/src/logo.svg reads",
    "ts:apps/shop/src/main.tsx#init -> css:apps/shop/src/index.css uses",
    "ts:apps/shop/src/main.tsx#init -> dep:npm:react-dom uses",
    "ts:apps/shop/src/map.ts#fn:startTiles -> ts:apps/shop/src/workers/tiles.worker.ts#fn:draw uses",
    "ts:apps/shop/src/map.ts#fn:startTiles -> ts:apps/shop/src/workers/tiles.worker.ts#init uses",
    "ts:apps/shop/src/map.ts#fn:startTiles -> ts:packages/money/src/rates.ts#const:RATES uses",
    "ts:apps/shop/src/map.ts#fn:startTiles -> ts:packages/money/src/rates.ts#fn:rate uses",
    "ts:apps/shop/src/workers/tiles.worker.ts#fn:draw -> ts:packages/money/src/rates.ts#const:RATES reads",
    "ts:packages/money/src/format.ts#fn:format -> ts:packages/money/src/rates.ts#const:RATES reads",
    "ts:packages/money/src/rates.ts#fn:rate -> ts:packages/money/src/rates.ts#const:RATES reads",
  ]);
  assert.deepEqual(cannot_tell, [
    {
      unit: "ts:apps/shop/src/pages/cart.tsx#fn:Cart",
      reason: "reads a name of ./nowhere that it does not export",
    },
    {
      unit: "ts:apps/shop/src/pages/cart.tsx#init",
      reason: "imports ./nowhere, which names no file of the tree",
    },
  ]);
});

test("wide links bind functions, whole files and types, each tagged with its narrowing", () => {
  const { links } = new Linker(tree, files).links();
  const s = new Set(show(links));
  for (const want of [
    "ts:apps/shop/src/app.tsx#fn:App -> ts:packages/money/src/format.ts#fn:format uses (ts-functions off)",
    "ts:apps/shop/src/app.tsx#fn:App -> ts:packages/money/src/rates.ts#fn:rate uses (ts-functions off)",
    "ts:apps/shop/src/app.tsx#fn:App -> ts:apps/shop/src/app.tsx#fn:Cart uses (ts-functions off)",
    "ts:apps/shop/src/app.tsx#fn:Cart -> ts:apps/shop/src/pages/cart.tsx#init uses (ts-functions off)",
    "ts:apps/shop/src/app.tsx#fn:App -> ts:apps/shop/src/app.tsx#init uses (ts-functions off)",
    "ts:apps/shop/src/app.tsx#init -> ts:apps/shop/src/app.tsx#fn:App uses (ts-functions off)",
    "ts:apps/shop/src/app.tsx#fn:App -> ts:apps/shop/src/types.ts#type:Line reads (types-erased off)",
    "ts:apps/shop/src/app.tsx#fn:App -> ts:apps/shop/src/app.tsx#const:TITLE reads",
  ])
    assert.ok(s.has(want), want);
});

test("why names the chain of links from one unit to another, every narrowing off", () => {
  const r = run(
    "why",
    JSON.stringify({
      protocol: 1,
      tree,
      claimed,
      from: "ts:apps/shop/src/main.tsx#init",
      to: "ts:packages/money/src/rates.ts#const:RATES",
    }),
  );
  assert.equal(r.code, 0);
  const path = (r.out as { path: { unit: string; note: string }[] }).path;
  assert.equal(path[0]?.unit, "ts:apps/shop/src/main.tsx#init");
  assert.equal(path.at(-1)?.unit, "ts:packages/money/src/rates.ts#const:RATES");
  assert.ok(path.length >= 3);
  const none = run(
    "why",
    JSON.stringify({
      protocol: 1,
      tree,
      claimed,
      from: "ts:packages/money/src/rates.ts#const:RATES",
      to: "ts:apps/shop/src/main.tsx#init",
    }),
  );
  assert.deepEqual(none.out, { path: [] });
});

test("whoever starts a worker binds every unit the worker runs, with or without narrowings", () => {
  const s = new Set(show(narrow(new Linker(tree, files).links().links)));
  for (const want of [
    "ts:apps/shop/src/map.ts#fn:startTiles -> ts:apps/shop/src/workers/tiles.worker.ts#fn:draw uses",
    "ts:apps/shop/src/map.ts#fn:startTiles -> ts:apps/shop/src/workers/tiles.worker.ts#init uses",
    "ts:apps/shop/src/map.ts#fn:startTiles -> ts:packages/money/src/rates.ts#fn:rate uses",
  ])
    assert.ok(s.has(want), want);
});

test("links come in one set per file, named by the files they were worked out from", () => {
  const l = new Linker(tree, files);
  const sets = l.sets();
  const app = sets.find((x) => x.group === "apps/shop/src/app.tsx");
  assert.ok(app);
  for (const f of [
    "apps/shop/src/app.tsx",
    "packages/money/src/index.ts",
    "packages/money/src/format.ts",
    "packages/money/package.json",
  ])
    assert.ok(app.inputs.includes(f), f);
  assert.ok(!app.inputs.includes("apps/shop/src/main.tsx"));
  assert.ok(app.links.every((x) => x.from.startsWith("ts:apps/shop/src/app.tsx#")));
  const asked = l.sets(["packages/money/src/format.ts"]).map((x) => x.group);
  assert.ok(asked.includes("apps/shop/src/app.tsx"));
  assert.ok(asked.includes("packages/money/src/format.ts"));
  assert.ok(!asked.includes("apps/shop/src/main.tsx"));
  assert.deepEqual(
    l.sets(["apps/shop/src/gone.ts"]).find((x) => x.group === "apps/shop/src/gone.ts"),
    {
      group: "apps/shop/src/gone.ts",
      inputs: [],
      absent: ["apps/shop/src/gone.ts"],
      whole: false,
      links: [],
      cannot_tell: [],
    },
  );
});
