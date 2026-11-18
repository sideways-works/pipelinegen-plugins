// Which files this correlator reads. The command answers detect with this module alone unless a
// tsconfig is among the paths, so it must not import TypeScript.
import { posix } from "node:path";
import {
  detect as detectScript,
  candidates as scriptCandidates,
} from "@sideways-works/ts-sbom/detect";

/** Files an import can name that are neither scripts nor stylesheets. */
export const assetExtensions = [
  "svg",
  "png",
  "jpg",
  "jpeg",
  "gif",
  "webp",
  "avif",
  "ico",
  "bmp",
  "json",
  "json5",
  "woff",
  "woff2",
  "ttf",
  "otf",
  "eot",
  "mp3",
  "mp4",
  "webm",
  "wav",
  "ogg",
  "wasm",
  "txt",
  "md",
  // a page a build starts from, and one an import reads as text
  "html",
];

export const candidates = [
  ...scriptCandidates,
  "**/tsconfig*.json",
  "**/package.json",
  ...assetExtensions.map((e) => `**/*.${e}`),
];

export const configName = /(?:^|\/)vite\.config\.(?:ts|mts|js|mjs)$/;
const tsconfigName = /(?:^|\/)tsconfig[^/]*\.json$/;
const packageName = /(?:^|\/)package\.json$/;
const assetName = new RegExp(`\\.(?:${assetExtensions.join("|")})$`);

export interface Detected {
  path: string;
  /**
   * `source`: a script or stylesheet whose imports are followed. `generated`: one a tool wrote.
   * `directive`: says how code is built or resolved. `manifest`: a package.json. `asset`: a file an
   * import can name.
   */
  role: "source" | "generated" | "directive" | "manifest" | "asset";
  /**
   * A recording can hold units of the file: a script, or a file of a `public` directory. Anything else
   * never shows in a recording, or not every time a test depends on it.
   */
  recorded?: true;
  /**
   * The build erases the file: a script of types alone. A build step that reads types makes it its
   * input, which that step's target must name: the claim can't know. Never with `recorded`.
   */
  erased?: true;
  tool?: string;
  /** Files the claim reads along: a tsconfig's `extends` chain. */
  depends?: string[];
}

/** Only TypeScript's own reader reads a tsconfig as TypeScript does. */
export function isTsconfig(path: string): boolean {
  return tsconfigName.test(path) && !path.split("/").includes("node_modules");
}

/**
 * Links resolve imports against the claimed files alone, so every tsconfig and every package.json
 * is claimed: either can change where an import leads. `jsonc` reads a tsconfig's text.
 */
export function detectWith(
  path: string,
  text: string,
  jsonc: (path: string, text: string) => unknown,
): Detected | undefined {
  const d = claimOf(path, text, jsonc);
  if (!d) return undefined;
  const script =
    (d.role === "source" || d.role === "generated") &&
    scriptName.test(path) &&
    !toolConfig.test(path) &&
    !buildTool.test(text) &&
    !typesOnly(text);
  const served = d.role === "asset" && /(?:^|\/)public\//.test(path);
  if (script || served) return { ...d, recorded: true };
  const erased =
    (d.role === "source" || d.role === "generated") && scriptName.test(path) && typesOnly(text);
  return erased ? { ...d, erased: true } : d;
}

const scriptName = /\.(?:[cm]?[jt]sx?)$/;

/**
 * Judged on the statements that start a line, as a formatter writes them: any other start counts as
 * code, so a file this misjudges is only ever left unmarked.
 */
export function typesOnly(text: string): boolean {
  // TypeScript and some builds follow a triple-slash reference.
  if (/^\s*\/\/\/\s*<reference\b/m.test(text)) return false;
  const code = text.replace(/\/\*[\s\S]*?\*\//g, "").replace(/(^|[^:"'`])\/\/[^\n]*/g, "$1");
  const starts = code.split("\n").filter((l) => l.trim() !== "" && !/^[\s})\]|&]/.test(l));
  return starts.length > 0 && starts.every((l) => typeStatement.test(l));
}

/** Not an enum, a namespace or module block, or any other import or export: those emit code. */
const typeStatement = new RegExp(
  [
    String.raw`^(?:export\s+)?(?:declare\s+)?interface\s+[A-Za-z_$][\w$]*`,
    String.raw`^(?:export\s+)?(?:declare\s+)?type\s+[A-Za-z_$][\w$]*\s*[<=]`,
    String.raw`^import\s+type\s+(?:\{|\*|[A-Za-z_$][\w$]*\s*(?:,|from\b))`,
    String.raw`^export\s+type\s+(?:\{|\*)`,
    String.raw`^export\s*\{\s*\}\s*;?\s*$`,
  ].join("|"),
);

const toolConfig = /(?:^|\/)[^/]+\.config\.[cm]?[jt]s$/;

/**
 * A script that imports a bundler (a type alone is enough) is build tooling: no recording holds it,
 * though it changes what the tests run.
 */
const buildTool =
  /(?:from\s*|require\(\s*|import\(\s*)["'](?:vite|rollup|rolldown|esbuild|webpack|@vitejs\/[^"']+|@rollup\/[^"']+|@rolldown\/[^"']+|vite-plugin-[^"']+|rollup-plugin-[^"']+)(?:\/[^"']*)?["']/;

function claimOf(
  path: string,
  text: string,
  jsonc: (path: string, text: string) => unknown,
): Detected | undefined {
  if (path.split("/").includes("node_modules")) return undefined;
  if (configName.test(path) && /from\s+["']vite["']|require\(\s*["']vite["']\s*\)/.test(text))
    return { path, role: "directive", tool: "vite" };
  const script = detectScript(path, text);
  if (script) return script;
  if (tsconfigName.test(path)) {
    const cfg = jsonc(path, text) as { extends?: unknown } | undefined;
    const ext = (Array.isArray(cfg?.extends) ? cfg.extends : [cfg?.extends]).filter(
      (e): e is string => typeof e === "string" && e.startsWith("."),
    );
    const depends = ext.map((e) => {
      const p = posix.normalize(posix.join(posix.dirname(path), e));
      return p.endsWith(".json") ? p : `${p}.json`;
    });
    return depends.length
      ? { path, role: "directive", tool: "typescript", depends }
      : { path, role: "directive", tool: "typescript" };
  }
  if (packageName.test(path)) return { path, role: "manifest" };
  if (assetName.test(path)) return { path, role: "asset" };
  return undefined;
}
