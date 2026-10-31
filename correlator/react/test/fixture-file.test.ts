import assert from "node:assert/strict";
import { mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { buildFixture } from "../build.ts";

const root = join(import.meta.dirname, "..");

test("the committed fixture is a build of these sources", async () => {
  const fresh = await buildFixture(mkdtempSync(join(tmpdir(), "react-correlator-fixture-")));
  assert.ok(
    readFileSync(join(root, "dist/playwright.mjs")).equals(readFileSync(fresh)),
    "dist/playwright.mjs differs from a build of the sources: run npm run build and commit it",
  );
});

test("the fixture is one self-contained module that imports only Node's own", () => {
  const text = readFileSync(join(root, "dist/playwright.mjs"), "utf8");
  const specifiers = [...text.matchAll(/(?:\bfrom\s*|\bimport\s*\(?\s*)["']([^"']+)["']/g)].map(
    (m) => m[1] as string,
  );
  assert.ok(specifiers.length > 0);
  // Imported from a path: nothing beside it to import, nothing a consumer must install.
  assert.deepEqual(
    specifiers.filter((s) => !s.startsWith("node:")),
    [],
  );
  assert.doesNotMatch(text, /\brequire\(/);
});

test("the release names the fixture as a file of its own", () => {
  const pkg = JSON.parse(readFileSync(join(root, "package.json"), "utf8"));
  assert.deepEqual(pkg.pipelinegen?.release, ["dist/playwright.mjs"]);
});
