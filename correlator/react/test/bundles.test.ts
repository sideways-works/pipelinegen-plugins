import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { build } from "esbuild";
import { buildIndex, type RawMap } from "../src/bundles.ts";
import { touchedIn } from "../src/observe.ts";

const root = join(import.meta.dirname, "..");
const site = "testdata/site";

async function bundle(sourcesContent: boolean): Promise<{ js: string; map: RawMap }> {
  const out = await build({
    entryPoints: { main: join(root, site, "src/main.ts") },
    bundle: true,
    format: "esm",
    minify: true,
    sourcemap: "external",
    sourcesContent,
    outdir: join(root, site, "dist/assets"),
    write: false,
    logLevel: "silent",
  });
  const text = (suffix: string) => {
    const f = out.outputFiles.find((o) => o.path.endsWith(suffix));
    assert.ok(f, `no ${suffix} in the build`);
    return f.text;
  };
  return { js: text("main.js"), map: JSON.parse(text("main.js.map")) as RawMap };
}

const read = (p: string): string | undefined => {
  try {
    return readFileSync(join(root, p), "utf8");
  } catch {
    return undefined;
  }
};

test("a map without its sources' text is read against the sources in the tree", async () => {
  const withText = await bundle(true);
  const without = await bundle(false);
  assert.equal(without.map.sourcesContent, undefined);
  assert.equal(without.js, withText.js);

  const jsPath = `${site}/dist/assets/main.js`;
  const a = buildIndex(jsPath, withText.js, withText.map, read);
  const b = buildIndex(jsPath, without.js, without.map, read);
  assert.ok(
    a.units.some((u) => u.startsWith("ts:")),
    `no source units: ${a.units.join(" ")}`,
  );
  assert.deepEqual(b, a);
  assert.deepEqual(b.stale, []);
});

test("only a map with the sources' text shows a source a build plugin changed", async () => {
  const withText = await bundle(true);
  const without = await bundle(false);
  const jsPath = `${site}/dist/assets/main.js`;
  const main = `${site}/src/main.ts`;
  // The tree's main.ts as it is now, the build made from an older text.
  const edited = (p: string) => {
    const t = read(p);
    return p === main && t !== undefined ? `// edited after the build\n${t}` : t;
  };
  assert.deepEqual(buildIndex(jsPath, withText.js, withText.map, edited).stale, [main]);
  assert.deepEqual(buildIndex(jsPath, without.js, without.map, edited).stale, []);
});

// The units a minified bundle's top level runs on load: the segments outside every function.
async function topLevel(files: Record<string, string>, entry: string): Promise<string[]> {
  const dir = mkdtempSync(join(tmpdir(), "bundle-"));
  for (const [p, text] of Object.entries(files)) {
    mkdirSync(dirname(join(dir, p)), { recursive: true });
    writeFileSync(join(dir, p), text);
  }
  const out = await build({
    entryPoints: [join(dir, entry)],
    bundle: true,
    format: "esm",
    minify: true,
    splitting: false,
    sourcemap: "external",
    outdir: join(dir, "dist"),
    write: false,
    logLevel: "silent",
  });
  const js = out.outputFiles.find((o) => o.path.endsWith(".js"))?.text ?? "";
  const map = JSON.parse(
    out.outputFiles.find((o) => o.path.endsWith(".map"))?.text ?? "{}",
  ) as RawMap;
  const read = (p: string) => {
    try {
      return readFileSync(join(dir, p), "utf8");
    } catch {
      return undefined;
    }
  };
  const index = buildIndex("dist/main.js", js, map, read);
  const units = new Set<string>();
  for (let i = 0; i < index.segs.length; i += 3)
    if ((index.segs[i + 1] as number) < 0)
      units.add(index.units[index.segs[i + 2] as number] as string);
  return [...units].sort();
}

test("loading a single bundle runs only the top level the modules really run", async () => {
  const units = await topLevel(
    {
      "src/main.ts":
        'import { Page } from "./page";\nexport const routes = [{ path: "/", render: Page }];\ndocument.addEventListener("click", async () => {\n  const { show } = await import("./lazy");\n  show();\n});\n',
      "src/page.ts":
        'export const Page = () => {\n  return "page " + (1 + 1);\n};\nexport const SIZES = [1, 2].map((n) => n * 2);\n',
      // A module the bundle initialises on first import: its glue and hoisted declarations run on load.
      "src/lazy.ts":
        'const COLORS = ["red", "green"];\nexport function show() {\n  return COLORS.length;\n}\n',
    },
    "src/main.ts",
  );
  assert.ok(units.includes("ts:src/main.ts#const:routes"), units.join(" "));
  assert.ok(units.includes("ts:src/page.ts#fn:SIZES"), units.join(" "));
  assert.ok(!units.includes("ts:src/page.ts#fn:Page"), units.join(" "));
  assert.ok(!units.some((u) => u.startsWith("ts:src/lazy.ts#")), units.join(" "));
});

test("a module the bundle starts on first import isn't started by loading the script", async () => {
  const dir = mkdtempSync(join(tmpdir(), "bundle-"));
  const files: Record<string, string> = {
    "src/main.ts":
      'export const ready = document.title.length;\ndocument.addEventListener("click", async () => {\n  const { show } = await import("./lazy");\n  show();\n});\n',
    "src/lazy.ts":
      'const COLORS = ["red", "green"].map((c) => c);\ndocument.body.dataset.lazy = "yes";\nexport function show() {\n  return COLORS.length;\n}\n',
  };
  for (const [p, text] of Object.entries(files)) {
    mkdirSync(dirname(join(dir, p)), { recursive: true });
    writeFileSync(join(dir, p), text);
  }
  const out = await build({
    entryPoints: [join(dir, "src/main.ts")],
    bundle: true,
    format: "esm",
    minify: true,
    splitting: false,
    sourcemap: "external",
    outdir: join(dir, "dist"),
    write: false,
    logLevel: "silent",
  });
  const js = out.outputFiles.find((o) => o.path.endsWith(".js"))?.text ?? "";
  const map = JSON.parse(
    out.outputFiles.find((o) => o.path.endsWith(".map"))?.text ?? "{}",
  ) as RawMap;
  const index = buildIndex("dist/main.js", js, map, (p) => readFileSync(join(dir, p), "utf8"));
  const touched = { ran: new Set<string>(), loaded: new Set<string>() };
  touchedIn(index, [[0, js.length]], [0], touched);
  const all = [...touched.loaded, ...touched.ran];
  assert.ok(all.includes("ts:src/main.ts#init"), all.join(" "));
  assert.ok(!all.some((u) => u.startsWith("ts:src/lazy.ts#")), all.join(" "));
});

test("a declaration-only module esbuild starts on first import counts, as nothing tells", async () => {
  const dir = mkdtempSync(join(tmpdir(), "bundle-"));
  const files: Record<string, string> = {
    "src/main.ts":
      'export const ready = document.title.length;\ndocument.addEventListener("click", async () => {\n  const { show } = await import("./lazy");\n  show();\n});\n',
    "src/lazy.ts":
      'import { words } from "./words";\nexport function show() {\n  return words().length;\n}\n',
    "src/words.ts": 'export function words() {\n  return ["a", "b"];\n}\n',
  };
  for (const [p, text] of Object.entries(files)) {
    mkdirSync(dirname(join(dir, p)), { recursive: true });
    writeFileSync(join(dir, p), text);
  }
  const out = await build({
    entryPoints: [join(dir, "src/main.ts")],
    bundle: true,
    format: "esm",
    minify: true,
    splitting: false,
    sourcemap: "external",
    outdir: join(dir, "dist"),
    write: false,
    logLevel: "silent",
  });
  const js = out.outputFiles.find((o) => o.path.endsWith(".js"))?.text ?? "";
  const map = JSON.parse(
    out.outputFiles.find((o) => o.path.endsWith(".map"))?.text ?? "{}",
  ) as RawMap;
  const index = buildIndex("dist/main.js", js, map, (p) => readFileSync(join(dir, p), "utf8"));
  const touched = { ran: new Set<string>(), loaded: new Set<string>() };
  touchedIn(index, [[0, js.length]], [0], touched);
  const all = [...touched.loaded, ...touched.ran];
  // esbuild hoists the declarations out of its lazy initialiser, which then holds only a call mapped
  // to the module's first position: no evidence it never ran, so the init counts (wider).
  assert.ok(all.includes("ts:src/lazy.ts#init"), all.join(" "));
  assert.ok(!all.includes("ts:src/lazy.ts#fn:show"), all.join(" "));
});
