// Builds the fixture site the way an app's bundler would: minified,
// code-split, with source maps that hold the sources' text.
import { cpSync, mkdirSync, rmSync } from "node:fs";
import { join } from "node:path";
import { build } from "esbuild";

export async function buildSite(site: string): Promise<void> {
  const dist = join(site, "dist");
  rmSync(dist, { recursive: true, force: true });
  mkdirSync(dist, { recursive: true });
  await build({
    entryPoints: { main: join(site, "src/main.ts") },
    bundle: true,
    splitting: true,
    format: "esm",
    minify: true,
    sourcemap: true,
    sourcesContent: true,
    entryNames: "[name]",
    chunkNames: "[name]-[hash]",
    outdir: join(dist, "assets"),
    logLevel: "silent",
  });
  cpSync(join(site, "index.html"), join(dist, "index.html"));
  cpSync(join(site, "public"), dist, { recursive: true });
}

/**
 * Builds the same site with Vite and its bundler (rolldown), the way an app
 * is built for a recording run: Vite's defaults, minified, with source maps
 * (`vite build --sourcemap`). `split` keeps the about page's dynamic import a
 * chunk of its own (routes split by page); without it every import is in
 * one script, as an app that imports its pages statically builds.
 */
export async function buildSiteVite(dir: string, split: boolean): Promise<string> {
  const { build: viteBuild } = await import("vite");
  const outDir = join(dir, split ? "dist-split" : "dist-whole");
  rmSync(outDir, { recursive: true, force: true });
  await viteBuild({
    configFile: false,
    root: dir,
    publicDir: join(dir, "../site/public"),
    logLevel: "silent",
    build: {
      outDir,
      emptyOutDir: true,
      sourcemap: true,
      ...(split ? {} : { rolldownOptions: { output: { codeSplitting: false } } }),
    },
  });
  return outDir;
}
