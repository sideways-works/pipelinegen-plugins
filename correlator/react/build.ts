// Builds the command (dist/react-correlator.mjs) and the Playwright fixture (dist/playwright.mjs).
// The fixture is committed so a git dependency needs no install script (test/fixture-file.test.ts
// checks it matches a fresh build).
//
// The command is one file: a small entry answers describe, collect and most detect calls, which
// the core makes often, and the rest, TypeScript and the linker included, is a CommonJS bundle
// compressed with brotli and embedded as base64, since loading it takes about a quarter of a
// second. scripts/check-release.ts compares a release's embedded part with a fresh build.
import { chmodSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { brotliCompressSync, brotliDecompressSync, constants } from "node:zlib";
import { build, formatMessagesSync, type Plugin } from "esbuild";

const root = import.meta.dirname;
const pkg = JSON.parse(readFileSync(join(root, "package.json"), "utf8"));
// ts-sbom is bundled from its sources, so the command holds exactly the pinned commit's code.
const tsSbom: Plugin = {
  name: "ts-sbom-sources",
  setup(b) {
    const src = join(root, "node_modules/@sideways-works/ts-sbom/src");
    b.onResolve({ filter: /^@sideways-works\/ts-sbom(?:\/detect)?$/ }, (a) => ({
      path: join(src, a.path.endsWith("/detect") ? "detect.ts" : "index.ts"),
    }));
  },
};
const shared = {
  absWorkingDir: root,
  bundle: true,
  platform: "node",
  target: "node22",
  logLevel: "silent",
} as const;
const define = { "process.env.REACT_CORRELATOR_VERSION": JSON.stringify(`v${pkg.version}`) };

/** What the embedded part's string starts with, so a checker can find it. */
export const embeddedTag = "embedded:brotli+base64:";

export async function embeddedBundle(): Promise<{ code: Uint8Array; legal: string }> {
  const r = await build({
    ...shared,
    define,
    entryPoints: ["src/cli.ts"],
    outfile: "embedded.cjs",
    format: "cjs",
    plugins: [tsSbom],
    minify: true,
    legalComments: "external",
    write: false,
  });
  // esbuild only warns that a CommonJS bundle has no import.meta or top-level await, which would
  // leave a command that fails at run time.
  if (r.warnings.length)
    throw new Error(formatMessagesSync(r.warnings, { kind: "warning" }).join("\n"));
  const code = r.outputFiles.find((f) => f.path.endsWith(".cjs"));
  if (!code) throw new Error("the embedded bundle wasn't written");
  const legal = r.outputFiles.find((f) => f.path.endsWith(".LEGAL.txt"))?.text ?? "";
  return { code: code.contents, legal };
}

/**
 * Brotli with every parameter fixed, so two builds give the same bytes. The bytes follow Node's
 * encoder, so `.node-version` pins it; the unpacked bytes don't depend on it.
 */
export function compress(code: Uint8Array): string {
  return brotliCompressSync(code, {
    params: {
      [constants.BROTLI_PARAM_MODE]: constants.BROTLI_MODE_TEXT,
      [constants.BROTLI_PARAM_QUALITY]: 11,
      [constants.BROTLI_PARAM_LGWIN]: 24,
    },
  }).toString("base64");
}

export function unpack(command: string): Buffer | undefined {
  const m = new RegExp(`"${embeddedTag.replace(/\+/g, "\\+")}([A-Za-z0-9+/=]*)"`).exec(command);
  return m?.[1] === undefined ? undefined : brotliDecompressSync(Buffer.from(m[1], "base64"));
}

// The module the entry's `import("./cli.ts")` becomes: it runs the embedded part as Node runs a
// CommonJS file. A part that can't be unpacked or run fails the call; it never answers empty.
function loader(packed: string): string {
  return `
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
const packed = ${JSON.stringify(embeddedTag + packed)};
const mod = { exports: {} };
try {
  const req = createRequire(import.meta.url);
  const file = fileURLToPath(import.meta.url);
  const code = req("node:zlib")
    .brotliDecompressSync(Buffer.from(packed.slice(${embeddedTag.length}), "base64"))
    .toString("utf8");
  req("node:vm").compileFunction(code, ["exports", "require", "module", "__filename", "__dirname"], {
    filename: file + " (embedded part)",
  })(mod.exports, req, mod, file, file.slice(0, file.lastIndexOf("/")));
} catch (e) {
  throw new Error("the command's embedded part could not be loaded: " + (e instanceof Error ? e.message : String(e)));
}
export const run = mod.exports.run;
`;
}

function embed(packed: string): Plugin {
  return {
    name: "embed",
    setup(b) {
      b.onResolve({ filter: /^\.\/cli\.ts$/ }, (a) =>
        a.importer.endsWith("/src/main.ts") ? { path: "cli", namespace: "embedded" } : undefined,
      );
      b.onLoad({ filter: /.*/, namespace: "embedded" }, () => ({
        contents: loader(packed),
        loader: "js",
      }));
    },
  };
}

export async function buildCommand(out: string): Promise<string> {
  const { code, legal } = await embeddedBundle();
  const file = join(out, "react-correlator.mjs");
  const r = await build({
    ...shared,
    define,
    entryPoints: ["src/main.ts"],
    outfile: file,
    format: "esm",
    minify: true,
    plugins: [tsSbom, embed(compress(code))],
    banner: { js: "#!/usr/bin/env node" },
    // The embedded part's license comments, readable without unpacking it.
    footer: legal ? { js: `/*! ${legal.replaceAll("/*", "(*").replaceAll("*/", "*)")}*/` } : {},
  });
  if (r.warnings.length)
    throw new Error(formatMessagesSync(r.warnings, { kind: "warning" }).join("\n"));
  chmodSync(file, 0o755);
  return file;
}

export async function buildFixture(out: string): Promise<string> {
  const file = join(out, "playwright.mjs");
  const r = await build({
    ...shared,
    entryPoints: ["src/playwright.ts"],
    outfile: file,
    format: "esm",
    external: ["@playwright/test"],
  });
  if (r.warnings.length)
    throw new Error(formatMessagesSync(r.warnings, { kind: "warning" }).join("\n"));
  return file;
}

if (process.argv[1] === import.meta.filename) {
  await buildCommand(join(root, "dist"));
  await buildFixture(join(root, "dist"));
}
