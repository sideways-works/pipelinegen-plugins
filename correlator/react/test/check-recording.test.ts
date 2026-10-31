import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { cpSync, mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

const script = join(import.meta.dirname, "..", "scripts", "check-recording.ts");
const tree = join(import.meta.dirname, "..", "testdata", "tree");

function git(dir: string, ...a: string[]) {
  const r = spawnSync(
    "git",
    ["-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", ...a],
    { encoding: "utf8" },
  );
  assert.equal(r.status, 0, r.stderr);
}

function repo(): string {
  const dir = mkdtempSync(join(tmpdir(), "check-"));
  cpSync(tree, dir, { recursive: true });
  git(dir, "init", "-q");
  git(dir, "add", ".");
  git(dir, "commit", "-q", "-m", "fixture");
  return dir;
}

function check(dir: string, coverage: string, ...more: string[]) {
  return spawnSync(
    process.execPath,
    [script, "--repo", dir, "--commit", "HEAD", "--coverage", coverage, ...more],
    {
      encoding: "utf8",
    },
  );
}

test("a recording of committed files passes", () => {
  const dir = repo();
  const r = check(dir, join(dir, "coverage"));
  assert.equal(r.status, 0, r.stdout + r.stderr);
});

test("each script v0.1.3 dropped is listed, and the exit code says so", () => {
  const dir = repo();
  const cov = mkdtempSync(join(tmpdir(), "cov-"));
  const part = join(cov, "s", "react", "1");
  mkdirSync(part, { recursive: true });
  writeFileSync(
    join(part, "node.json"),
    JSON.stringify({
      version: 1,
      scripts: [
        {
          file: "packages/money/dist/format.js",
          source: "",
          ran: [],
          fns: [],
          map: { sources: ["packages/money/src/format.ts", "packages/money/src/gen.ts"] },
        },
        { file: "packages/money/dist/rates.js", source: "", ran: [], fns: [] },
      ],
    }),
  );
  writeFileSync(join(part, "done"), "");
  const dist = mkdtempSync(join(tmpdir(), "dist-"));
  mkdirSync(join(dist, "assets"));
  writeFileSync(join(dist, "assets", "main.js"), "console.log(1)\n");
  writeFileSync(
    join(part, "browser.json"),
    JSON.stringify({
      version: 1,
      browser: "chromium",
      supported: true,
      scripts: [{ url: "http://localhost:5173/assets/main.js", sha: "", ran: [], fns: [] }],
      requests: [],
    }),
  );
  const r = check(dir, cov, "--build", dist);
  assert.equal(r.status, 1, r.stdout + r.stderr);
  for (const want of [
    "node script packages/money/dist/format.js is not in the commit: v0.1.3 dropped it; v0.1.4 reads it through its map",
    "node script packages/money/dist/rates.js is not in the commit and has no map: v0.1.3 dropped it; v0.1.4 can't tell",
    "note: map source packages/money/src/gen.ts (of packages/money/dist/format.js) is not in the commit: binds nothing",
    "browser script http://localhost:5173/assets/main.js (",
    "has no source map, nor has any script of its build: v0.1.3 dropped it; v0.1.4 can't tell",
  ])
    assert.ok(r.stdout.includes(want), `${want} missing from:\n${r.stdout}`);
});
