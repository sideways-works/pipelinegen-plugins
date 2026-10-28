// The committed library files must be exactly what the sources build to.
import assert from "node:assert/strict";
import { mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { buildLibrary, library } from "../build.ts";

const root = join(import.meta.dirname, "..");

test("the committed library files are a build of these sources", async () => {
  const out = mkdtempSync(join(tmpdir(), "ts-sbom-library-"));
  await buildLibrary(out);
  for (const [file] of library)
    assert.ok(
      readFileSync(join(root, "dist", file)).equals(readFileSync(join(out, file))),
      `dist/${file} differs from a build of the sources: run npm run build and commit it`,
    );
});

test("detection loads no parser", async () => {
  const text = readFileSync(join(root, "dist/detect.mjs"), "utf8");
  assert.doesNotMatch(text, /from\s*["']typescript["']/);
  const detect = await import(join(root, "dist/detect.mjs"));
  assert.deepEqual(detect.detect("src/a.ts", "export {};\n"), { path: "src/a.ts", role: "source" });
});
