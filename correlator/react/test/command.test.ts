// describe, collect and a detect without a tsconfig must answer without TypeScript and the linker,
// and the built file must carry exactly the code of these sources.
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { before, test } from "node:test";
import { buildCommand, compress, embeddedTag } from "../build.ts";
import { run } from "../src/cli.ts";
import { firstPartDir } from "../src/record.ts";
import { claimedIn } from "./claimed.ts";

const root = join(import.meta.dirname, "..");
const pkg = JSON.parse(readFileSync(join(root, "package.json"), "utf8")) as { version: string };
const empty = mkdtempSync(join(tmpdir(), "react-correlator-slice-"));
const requests: Record<string, [string, string]> = {
  describe: ["describe", ""],
  collect: [
    "collect",
    JSON.stringify({ protocol: 1, action: "slice-end", slice: "a.spec.ts", dir: empty }),
  ],
  "collect with a stack": [
    "collect",
    JSON.stringify({ protocol: 1, action: "slice-end", dir: empty, stack: true }),
  ],
  detect: [
    "detect",
    JSON.stringify({
      protocol: 1,
      tree: root,
      paths: [
        "testdata/site/vite.config.ts",
        "testdata/tree/package.json",
        "testdata/tree/packages/money/package.json",
      ],
    }),
  ],
  "detect with a tsconfig": [
    "detect",
    JSON.stringify({
      protocol: 1,
      tree: root,
      paths: ["testdata/tree/packages/money/package.json", "testdata/tree/apps/shop/tsconfig.json"],
    }),
  ],
  links: [
    "links",
    JSON.stringify({
      protocol: 1,
      tree: join(root, "testdata/tree"),
      claimed: claimedIn(join(root, "testdata/tree")),
    }),
  ],
};
const light = ["describe", "collect", "collect with a stack", "detect"];
// Makes unpacking the embedded part fail, to see which calls need it.
const trap = `data:text/javascript,${encodeURIComponent(
  'import zlib from "node:zlib"; zlib.brotliDecompressSync = () => { throw new Error("trapped"); };',
)}`;

function node(args: string[], input: string) {
  return spawnSync(process.execPath, args, { cwd: root, input, encoding: "utf8" });
}

function request(name: string): [string, string] {
  const r = requests[name];
  if (!r) throw new Error(`no request ${name}`);
  return r;
}

function loads(name: string): { src: string[]; packages: string[] } {
  const [call, input] = request(name);
  const r = node(["--import", "./test/trace-loads.ts", "src/main.ts", call], input);
  const urls = [...r.stderr.matchAll(/^loaded (.*)$/gm)].map((m) => m[1] as string);
  const prefix = new URL(`file://${root}/`).href;
  return {
    src: urls
      .filter((u) => u.startsWith(`${prefix}src/`))
      .map((u) => u.slice(prefix.length))
      .sort(),
    packages: [
      ...new Set(
        urls.filter((u) => u.includes("/node_modules/")).map((u) => u.split("/node_modules/")[1]),
      ),
    ].sort() as string[],
  };
}

test("describe, collect and detect without a tsconfig load no TypeScript and no linker", () => {
  const src = ["src/call.ts", "src/detect.ts", "src/main.ts", "src/narrowings.ts", "src/record.ts"];
  // ts-sbom's detection alone, which loads no parser.
  const packages = ["@sideways-works/ts-sbom/dist/detect.mjs"];
  for (const name of light) assert.deepEqual(loads(name), { src, packages }, name);
  // The trace sees what the other calls load, so the check above can fail.
  const tsconfig = loads("detect with a tsconfig");
  assert.ok(tsconfig.src.includes("src/builds.ts"));
  assert.ok(tsconfig.packages.some((p) => p.startsWith("typescript/")));
  const links = loads("links");
  assert.ok(links.src.includes("src/links.ts"));
  assert.ok(links.packages.some((p) => p.startsWith("typescript/")));
});

test("describe declares only the collect action that has work to do", () => {
  const out = run("describe", "").out as { collects?: string[] };
  assert.deepEqual(out.collects, ["slice-end"]);
});

test("describe names build_root as a path observe reads where it runs", () => {
  const out = run("describe", "").out as { observe_reads?: string[] };
  assert.deepEqual(out.observe_reads, ["build_root"]);
});

let command = "";

before(async () => {
  command = await buildCommand(mkdtempSync(join(tmpdir(), "react-correlator-build-")));
});

test("the built command unpacks its embedded part only for the calls that need it", () => {
  for (const name of Object.keys(requests)) {
    const [call, input] = request(name);
    const want = run(call, input);
    // The build names its version; the sources run as a development version.
    if (call === "describe") (want.out as { version: string }).version = `v${pkg.version}`;
    const plain = node([command, call], input);
    assert.equal(plain.status, want.code, `${name}: ${plain.stderr}`);
    assert.deepEqual(plain.stdout ? JSON.parse(plain.stdout) : undefined, want.out, name);
    const trapped = node(["--import", trap, command, call], input);
    if (light.includes(name)) {
      assert.equal(trapped.stdout, plain.stdout, name);
      assert.equal(trapped.status, plain.status, name);
      continue;
    }
    assert.equal(trapped.status, 1, name);
    assert.equal(trapped.stdout, "", name);
    assert.match(trapped.stderr, /embedded part could not be loaded: trapped/, name);
  }
});

test("check-release accepts the build of these sources and nothing else", () => {
  const ok = node(["scripts/check-release.ts", command], "");
  assert.equal(ok.status, 0, ok.stderr);
  const text = readFileSync(command, "utf8");
  const other = join(
    mkdtempSync(join(tmpdir(), "react-correlator-other-")),
    "react-correlator.mjs",
  );
  writeFileSync(
    other,
    text.replace(
      new RegExp(`"${embeddedTag.replace(/\+/g, "\\+")}[A-Za-z0-9+/=]*"`),
      JSON.stringify(
        embeddedTag + compress(Buffer.from("exports.run = () => ({ code: 0, out: {} });")),
      ),
    ),
  );
  const bad = node(["scripts/check-release.ts", other], "");
  assert.equal(bad.status, 1);
  assert.match(bad.stderr, /differs from a build of these sources/);
});

test("only the core's directory for a later part is read as one", () => {
  assert.equal(
    firstPartDir("/c/.pipelinegen/part-2/e2e/a.spec.ts/react", "e2e/a.spec.ts"),
    "/c/e2e/a.spec.ts/react",
  );
  assert.equal(firstPartDir("/c/e2e/a.spec.ts/react", "e2e/a.spec.ts"), undefined);
  assert.equal(
    firstPartDir("/c/.pipelinegen/part-1/e2e/a.spec.ts/react", "e2e/a.spec.ts"),
    undefined,
  );
  assert.equal(firstPartDir("/c/other/part-2/e2e/a.spec.ts/react", "e2e/a.spec.ts"), undefined);
  assert.equal(
    firstPartDir("/c/.pipelinegen/part-2/e2e/b.spec.ts/react", "e2e/a.spec.ts"),
    undefined,
  );
});
