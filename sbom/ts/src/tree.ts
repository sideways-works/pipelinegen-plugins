// File summaries, cached by content so two trees that share a file parse it once.
import { createHash } from "node:crypto";
import { mkdirSync, readFileSync, renameSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { cssDigest } from "./css.ts";
import { isBuilt } from "./detect.ts";
import { isScript, isStyle } from "./ids.ts";
import { type FileSummary, summarize } from "./units.ts";

/** Bumped whenever a summary's shape or a digest's input changes. */
export const summaryVersion = "6";

export function listFiles(files: Iterable<string>): string[] {
  return [...new Set(files)].filter((f) => isScript(f) || isStyle(f)).sort();
}

export type Summary = FileSummary | { css: string };

function key(path: string, text: string): string {
  // The extension decides the parser (JSX or not).
  const ext = path.slice(path.lastIndexOf("."));
  return createHash("sha256").update(`${summaryVersion}\0${ext}\0${text}`).digest("hex");
}

export function summaryOf(path: string, text: string, cache?: string): Summary {
  const k = key(path, text);
  const file = cache ? join(cache, "summaries", k.slice(0, 2), `${k}.json`) : "";
  if (cache) {
    try {
      return JSON.parse(readFileSync(file, "utf8")) as Summary;
    } catch {
      // not cached yet
    }
  }
  const s: Summary = isStyle(path) ? { css: cssDigest(text) } : summarize(path, text);
  if (cache) {
    mkdirSync(join(cache, "summaries", k.slice(0, 2)), { recursive: true });
    // Written aside and renamed, so a reader never sees half a file.
    const tmp = `${file}.${process.pid}.tmp`;
    writeFileSync(tmp, JSON.stringify(s));
    renameSync(tmp, file);
  }
  return s;
}

export function readTree(
  root: string,
  files: Iterable<string>,
  cache?: string,
): Map<string, Summary> {
  const out = new Map<string, Summary>();
  for (const f of listFiles(files)) {
    let text: string;
    try {
      text = readFileSync(join(root, f), "utf8");
    } catch {
      continue; // claimed, but missing in the work tree
    }
    // A minified bundle checked into the tree is build output, not source.
    if (isBuilt(f, text)) continue;
    out.set(f, summaryOf(f, text, cache));
  }
  return out;
}
