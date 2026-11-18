// describe and detect must answer without the TypeScript parser, and the built file must carry
// exactly the code of these sources.
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { before, test } from "node:test";
import { buildCommand } from "../build.ts";
import { run } from "../src/cli.ts";
import { claimedIn } from "./claimed.ts";

const root = join(import.meta.dirname, "..");
const pkg = JSON.parse(readFileSync(join(root, "package.json"), "utf8")) as { version: string };
const tree = join(root, "testdata/app");
const requests: Record<string, string> = {
  describe: "",
  detect: JSON.stringify({ protocol: 1, tree, paths: ["src/cart.tsx", "src/cart.css"] }),
  units: JSON.stringify({ protocol: 1, tree, claimed: claimedIn(tree) }),
};
// Makes unpacking the embedded part fail, to see which calls need it.
const trap = `data:text/javascript,${encodeURIComponent(
  'import zlib from "node:zlib"; zlib.brotliDecompressSync = () => { throw new Error("trapped"); };',
)}`;

function node(args: string[], input: string) {
  return spawnSync(process.execPath, args, { cwd: root, input, encoding: "utf8" });
}

function loads(call: string): { src: string[]; packages: boolean } {
  const r = node(["--import", "./test/trace-loads.ts", "src/main.ts", call], requests[call] ?? "");
  assert.equal(r.status, 0, r.stderr);
  const urls = [...r.stderr.matchAll(/^loaded (.*)$/gm)].map((m) => m[1] as string);
  const prefix = new URL(`file://${root}/`).href;
  return {
    src: urls
      .filter((u) => u.startsWith(`${prefix}src/`))
      .map((u) => u.slice(prefix.length))
      .sort(),
    packages: urls.some((u) => u.includes("/node_modules/")),
  };
}

test("describe and detect load neither the parser nor the modules that use it", () => {
  const light = ["src/call.ts", "src/detect.ts", "src/ids.ts", "src/main.ts"];
  for (const call of ["describe", "detect"])
    assert.deepEqual(loads(call), { src: light, packages: false }, call);
  // The trace sees what units loads, so the check above can fail.
  const units = loads("units");
  assert.ok(units.src.includes("src/cli.ts") && units.src.includes("src/units.ts"));
  assert.ok(units.packages);
});

let command = "";

before(async () => {
  command = await buildCommand(mkdtempSync(join(tmpdir(), "ts-sbom-build-")));
});

test("the built command unpacks its embedded part only for units", () => {
  for (const call of ["describe", "detect"]) {
    const r = node(["--import", trap, command, call], requests[call] ?? "");
    assert.equal(r.status, 0, r.stderr);
    // The build names its version; the sources run as a development version.
    const want = run(call, requests[call] ?? "").out as Record<string, unknown>;
    if (call === "describe") want.version = `v${pkg.version}`;
    assert.deepEqual(JSON.parse(r.stdout), want, call);
  }
  const plain = node([command, "units"], requests.units ?? "");
  assert.equal(plain.status, 0, plain.stderr);
  assert.deepEqual(JSON.parse(plain.stdout), run("units", requests.units ?? "").out);
  const trapped = node(["--import", trap, command, "units"], requests.units ?? "");
  assert.equal(trapped.status, 1);
  assert.equal(trapped.stdout, "");
  assert.match(trapped.stderr, /embedded part could not be loaded: trapped/);
});
