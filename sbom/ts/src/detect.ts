// Which files are this plugin's: a file is claimed only on positive evidence.
import { isScript, isStyle } from "./ids.ts";

export const candidates = [
  "**/*.ts",
  "**/*.tsx",
  "**/*.mts",
  "**/*.cts",
  "**/*.js",
  "**/*.jsx",
  "**/*.mjs",
  "**/*.cjs",
  "**/*.css",
];

export type Role = "source" | "generated";

export interface Detected {
  path: string;
  role: Role;
  /** For generated files: the tool the header names, when it names one. */
  tool?: string;
}

const generatedHeader =
  /^(?:\s*(?:\/\/|\/\*+|\*|#)[^\n]*\n){0,5}?\s*(?:\/\/|\/\*+|\*)\s*(?:Code generated(?: by ([^\s.]+))?|@generated|This file (?:was|is) (?:auto(?:matically)?[- ])?generated(?: by ([^\s.]+))?|DO NOT EDIT)/i;

/** A minified bundle checked into the tree: a first line far longer than anyone writes. */
export function isBuilt(path: string, text: string): boolean {
  const firstLine = text.slice(0, text.indexOf("\n") >>> 0);
  return firstLine.length > 5000 && /\.(?:m?js|cjs|css)$/.test(path);
}

/**
 * A script, a stylesheet, or one a tool generated (still listed: its code runs like any other).
 * Build output checked into the tree is undefined.
 */
export function detect(path: string, text: string): Detected | undefined {
  if (path.split("/").includes("node_modules")) return undefined;
  if (!isScript(path) && !isStyle(path)) return undefined;
  if (isBuilt(path, text)) return undefined;
  const g = generatedHeader.exec(text.slice(0, 2000));
  if (g) {
    const tool = g[1] ?? g[2];
    return tool ? { path, role: "generated", tool } : { path, role: "generated" };
  }
  return { path, role: "source" };
}
