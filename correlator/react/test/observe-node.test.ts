import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { transformSync } from "esbuild";
import { run } from "../src/cli.ts";
import { CannotTell, observe } from "../src/observe.ts";

const tree = join(import.meta.dirname, "..", "testdata", "tree");
const dirs = [join(tree, "coverage/_setup/react"), join(tree, "coverage/money/react")];
const format = "packages/money/src/format.ts";

test("a Node recording of the tree's own file names the units that ran", () => {
  const got = observe(tree, dirs);
  const of = (ids: string[]) => ids.filter((id) => id.startsWith(`ts:${format}#`));
  assert.deepEqual(of(got.touched), [`ts:${format}#fn:format`, `ts:${format}#init`]);
  assert.deepEqual(of(got.setup_like), [`ts:${format}#init`]);
  assert.deepEqual(got.missing, []);
  for (const f of ["apps/shop/src/app.tsx", "apps/shop/src/map.ts", "packages/money/src/rates.ts"])
    assert.ok(
      got.touched.some((id) => id.startsWith(`ts:${f}#`)),
      f,
    );
});

test("a Node recording of a file the tree lacks, without a map, can't be told", () => {
  const other = mkdtempSync(join(tmpdir(), "tree-"));
  assert.throws(() => observe(other, dirs), CannotTell);
  assert.throws(
    () => observe(other, dirs),
    /the tree has no such file and the record no source map/,
  );
});

function part(scripts: unknown[]): string {
  const dir = mkdtempSync(join(tmpdir(), "part-"));
  mkdirSync(join(dir, "1"));
  writeFileSync(join(dir, "1", "node.json"), JSON.stringify({ version: 1, scripts }));
  writeFileSync(join(dir, "1", "done"), "");
  return dir;
}

// built is format.ts as a workspace package's build output: a file the tree doesn't hold, with a
// map to the committed source.
function built(sources: string[]) {
  const text = readFileSync(join(tree, format), "utf8");
  const out = transformSync(text, {
    loader: "ts",
    format: "esm",
    sourcemap: "external",
    sourcefile: "x.ts",
  });
  const map = JSON.parse(out.map);
  map.sources = sources;
  delete map.sourcesContent;
  const fn = out.code.indexOf("function format");
  return {
    file: "packages/money/dist/format.js",
    source: out.code,
    map,
    ran: [[0, out.code.length]],
    fns: [0, fn],
  };
}

test("a Node script the tree lacks is read through its map into the committed sources", () => {
  const got = observe(tree, [join(tree, "coverage/_setup/react"), part([built([format])])]);
  assert.ok(got.touched.includes(`ts:${format}#fn:format`), got.touched.join(" "));
  assert.deepEqual(got.missing, []);
});

test("map sources the tree lacks bind nothing, and observe says so", () => {
  const dir = part([built(["packages/money/src/generated.ts"])]);
  const got = observe(tree, [join(tree, "coverage/_setup/react"), dir]);
  assert.deepEqual(got.missing, ["packages/money/src/generated.ts"]);
  const r = run(
    "observe",
    JSON.stringify({ protocol: 1, tree, dirs: [join(tree, "coverage/_setup/react"), dir] }),
  );
  assert.equal(r.code, 0);
  assert.match(
    r.err ?? "",
    /1 map sources of scripts that ran are not in the tree.*packages\/money\/src\/generated\.ts/,
  );
  assert.equal(
    JSON.stringify(r.out).includes("missing"),
    false,
    "the answer keeps to the protocol",
  );
});

test("observe names on stderr the installed packages a slice touched", () => {
  const dir = part([built(["node_modules/left-pad/index.js"])]);
  const r = run(
    "observe",
    JSON.stringify({ protocol: 1, tree, dirs: [join(tree, "coverage/_setup/react"), dir] }),
  );
  assert.equal(r.code, 0);
  assert.match(
    r.err ?? "",
    /touched 1 units of installed packages, which an sbom of the lockfile must list: dep:npm:left-pad/,
  );
});
