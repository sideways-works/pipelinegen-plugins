// A file appearing where an import looked and found nothing changes where the import leads, while
// every input stays the same: asking about such a path must answer the set.
import assert from "node:assert/strict";
import { existsSync, mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { Linker, type LinkSet } from "../src/links.ts";
import { claimedPaths } from "./claimed.ts";

function tree(files: Record<string, string>): string {
  const root = mkdtempSync(join(tmpdir(), "react-correlator-shadow-"));
  write(root, files);
  return root;
}

function write(root: string, files: Record<string, string>): void {
  for (const [p, text] of Object.entries(files)) {
    mkdirSync(dirname(join(root, p)), { recursive: true });
    writeFileSync(join(root, p), text);
  }
}

function existing(root: string, s: LinkSet): void {
  for (const i of s.inputs)
    assert.ok(existsSync(join(root, i)), `input ${i} is no file of the tree`);
}

function answers(root: string, path: string, group: string): void {
  const got = new Linker(root, claimedPaths(root)).sets([path]).map((x) => x.group);
  assert.ok(got.includes(group), `asking about ${path} answers ${got.join(", ")}, not ${group}`);
}

function setOf(root: string, group: string): LinkSet {
  const set = new Linker(root, claimedPaths(root)).sets().find((s) => s.group === group);
  assert.ok(set, group);
  return set;
}

const targets = (s: LinkSet) => s.links.map((l) => l.to).sort();

test("a script added beside the one an import found takes the import over", () => {
  const root = tree({
    "src/main.ts": 'import { x } from "./x";\nexport const y = x;\n',
    "src/x.tsx": "export const x = 1;\n",
  });
  const before = setOf(root, "src/main.ts");
  assert.ok(targets(before).includes("ts:src/x.tsx#const:x"));
  // `./x` tries x.ts before x.tsx.
  existing(root, before);
  assert.ok(!before.inputs.includes("src/x.ts"));
  assert.ok(before.absent.includes("src/x.ts"));
  answers(root, "src/x.ts", "src/main.ts");
  write(root, { "src/x.ts": "export const x = 2;\n" });
  const after = setOf(root, "src/main.ts");
  assert.ok(targets(after).includes("ts:src/x.ts#const:x"));
  assert.ok(!targets(after).includes("ts:src/x.tsx#const:x"));
});

test("an index file added under a directory an import named resolves it", () => {
  const root = tree({
    "src/main.ts": 'import { w } from "./widgets";\nexport const y = w;\n',
  });
  const before = setOf(root, "src/main.ts");
  assert.ok(before.cannot_tell.length > 0);
  existing(root, before);
  assert.ok(before.absent.includes("src/widgets/index.ts"));
  answers(root, "src/widgets/index.ts", "src/main.ts");
  write(root, { "src/widgets/index.ts": "export const w = 1;\n" });
  const after = setOf(root, "src/main.ts");
  assert.deepEqual(after.cannot_tell, []);
  assert.ok(targets(after).includes("ts:src/widgets/index.ts#const:w"));
});

test("a package.json that gains a name turns an npm import into a workspace one", () => {
  const root = tree({
    "app/main.ts": 'import { b } from "kit";\nexport const y = b;\n',
    "kit/package.json": "{}\n",
    "kit/index.ts": "export const b = 1;\n",
  });
  const before = setOf(root, "app/main.ts");
  assert.ok(targets(before).includes("dep:npm:kit"));
  assert.ok(before.inputs.includes("kit/package.json"));
  write(root, { "kit/package.json": '{"name": "kit", "main": "index.ts"}\n' });
  const after = setOf(root, "app/main.ts");
  assert.ok(!targets(after).includes("dep:npm:kit"));
  assert.ok(targets(after).includes("ts:kit/index.ts#const:b"));
});

test("a tsconfig added nearer to an importer than the one it used is an input", () => {
  const root = tree({
    "tsconfig.json": '{"compilerOptions": {"paths": {"@/*": ["src/*"]}}}\n',
    "app/src/main.ts": 'import { a } from "@/a";\nexport const y = a;\n',
    "src/a.ts": "export const a = 1;\n",
  });
  const before = setOf(root, "app/src/main.ts");
  assert.ok(targets(before).includes("ts:src/a.ts#const:a"));
  existing(root, before);
  assert.ok(before.inputs.includes("tsconfig.json"));
  for (const t of ["app/src/tsconfig.json", "app/tsconfig.json"])
    assert.ok(before.absent.includes(t), `${t} is not listed absent: ${before.absent.join(", ")}`);
  answers(root, "app/src/tsconfig.json", "app/src/main.ts");
  answers(root, "app/tsconfig.json", "app/src/main.ts");
});

test("an import without its extension, an index file and a workspace package are inputs as files", () => {
  const root = tree({
    "package.json": '{"name": "root", "workspaces": ["kit"]}\n',
    "kit/package.json": '{"name": "kit", "exports": {".": "./src/index.ts"}}\n',
    "kit/src/index.ts": "export const b = 1;\n",
    "e2e/fixtures.ts": "export const f = 1;\n",
    "e2e/pages/index.tsx": "export const p = 1;\n",
    "e2e/auth.spec.ts":
      'import { f } from "./fixtures";\nimport { p } from "./pages";\nimport { b } from "kit";\nimport { r } from "react";\nexport const y = [f, p, b, r];\n',
  });
  const s = setOf(root, "e2e/auth.spec.ts");
  existing(root, s);
  for (const f of [
    "e2e/fixtures.ts",
    "e2e/pages/index.tsx",
    "kit/src/index.ts",
    "kit/package.json",
  ])
    assert.ok(s.inputs.includes(f), `${f} is not an input: ${s.inputs.join(", ")}`);
  for (const f of ["e2e/fixtures", "e2e/pages", "react"]) assert.ok(!s.inputs.includes(f), f);
  assert.ok(s.absent.includes("e2e/tsconfig.json"), s.absent.join(", "));
  assert.ok(s.absent.includes("e2e/fixtures"));
  for (const a of s.absent) assert.ok(!existsSync(join(root, a)), `${a} exists`);
  assert.ok(targets(s).includes("dep:npm:react"));
});

test("a solution tsconfig's referenced project gives its files their paths", () => {
  const root = tree({
    "web/tsconfig.json":
      '{"files": [], "references": [{"path": "./tsconfig.app.json"}, {"path": "./tsconfig.node.json"}]}\n',
    "web/tsconfig.app.json":
      '{"compilerOptions": {"paths": {"@/*": ["./src/*"]}}, "include": ["src"], "exclude": ["src/**/*.test.ts"]}\n',
    "web/tsconfig.node.json": '{"compilerOptions": {}, "include": ["vite.config.ts"]}\n',
    "web/src/shared/index.ts": "export const s = 1;\n",
    "web/src/entities/api.ts": 'import { s } from "@/shared";\nexport const a = s;\n',
    "web/src/entities/api.test.ts": 'import { s } from "@/shared";\nexport const t = s;\n',
  });
  for (const f of ["web/src/entities/api.ts", "web/src/entities/api.test.ts"]) {
    const s = setOf(root, f);
    assert.ok(
      targets(s).includes("ts:web/src/shared/index.ts#const:s"),
      `${f}: ${targets(s).join(" ")}`,
    );
    assert.ok(!targets(s).some((t) => t.startsWith("dep:npm:")), targets(s).join(" "));
    assert.deepEqual(s.cannot_tell, []);
  }
});

test("a baseUrl without paths resolves bare imports under it before packages", () => {
  const root = tree({
    "tsconfig.json": '{"compilerOptions": {"baseUrl": "src"}}\n',
    "src/shared/x.ts": "export const x = 1;\n",
    "src/main.ts":
      'import { x } from "shared/x";\nimport { y } from "lodash";\nexport const m = [x, y];\n',
  });
  const s = setOf(root, "src/main.ts");
  assert.ok(targets(s).includes("ts:src/shared/x.ts#const:x"), targets(s).join(" "));
  assert.ok(targets(s).includes("dep:npm:lodash"), targets(s).join(" "));
});

test("a bundler-only alias is no package: the reason says to declare it", () => {
  const root = tree({
    "src/shared/index.ts": "export const s = 1;\n",
    "src/main.ts":
      'import { s } from "@/shared";\nimport { t } from "~/tools";\nexport const m = [s, t];\n',
  });
  const s = setOf(root, "src/main.ts");
  assert.ok(!targets(s).some((t) => t.startsWith("dep:npm:")), targets(s).join(" "));
  const reasons = s.cannot_tell.map((c) => c.reason).join("\n");
  assert.match(
    reasons,
    /import '@\/shared' matches no package name and no tsconfig path: declare the alias under the plugin's config `aliases`/,
  );
  assert.match(reasons, /import '~\/tools'/);
  const declared = new Linker(root, claimedPaths(root), { aliases: { "@": "src" } })
    .sets()
    .find((x) => x.group === "src/main.ts");
  assert.ok(declared && targets(declared).includes("ts:src/shared/index.ts#const:s"));
});
