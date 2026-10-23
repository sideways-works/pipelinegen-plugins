// What the Playwright fixture leaves in a slice's directory, and observe reads. A slice or the
// setup can run more than once (once per project): each run is a numbered part, so a later one never
// overwrites an earlier one.
import {
  existsSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  renameSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { basename, dirname, join } from "node:path";

/** Written last in a part: a part without it was not finished and is not read. */
export const doneFile = "done";
/** In the setup's directory: each served script once as `<sha>.js`, with `<sha>.map` or `<sha>.nomap`. */
export const scriptsDir = "scripts";
/** Beside a kept script whose source map couldn't be fetched, holding why. */
export const mapFailSuffix = ".mapfail";
export const browserFile = "browser.json";
export const nodeFile = "node.json";

/** Creating the directory claims the number, so two writers never share a part. */
export function newPart(dir: string): string {
  mkdirSync(dir, { recursive: true });
  for (let n = 1; ; n++) {
    const part = join(dir, String(n));
    try {
      mkdirSync(part);
      return part;
    } catch (e) {
      if ((e as NodeJS.ErrnoException).code !== "EEXIST") throw e;
    }
  }
}

function isDir(p: string): boolean {
  try {
    return statSync(p).isDirectory();
  } catch {
    return false;
  }
}

/** In the order written; the directory itself counts when it holds a part's files directly. */
export function partsOf(dir: string): string[] {
  if (!isDir(dir)) return [];
  const names = readdirSync(dir);
  const numbered = names
    .filter((n) => /^[1-9][0-9]*$/.test(n) && isDir(join(dir, n)))
    .sort((a, b) => Number(a) - Number(b))
    .map((n) => join(dir, n));
  const flat = [doneFile, browserFile, nodeFile].some((f) => names.includes(f)) ? [dir] : [];
  return [...flat, ...numbered];
}

/**
 * The directory the fixture wrote a later part from, when dir is the core's directory for it:
 * `<root>/.pipelinegen/part-<k>/<slice>/<name>` (k ≥ 2) for `<root>/<slice>/<name>`.
 */
export function firstPartDir(dir: string, slice: string): string | undefined {
  const name = basename(dir);
  const sliceDir = dirname(dir);
  const tail = slice.split("/").filter(Boolean);
  let p = sliceDir;
  for (const seg of [...tail].reverse()) {
    if (basename(p) !== seg) return undefined;
    p = dirname(p);
  }
  if (!/^part-([2-9]|[1-9][0-9]+)$/.test(basename(p))) return undefined;
  if (basename(dirname(p)) !== partsRoot) return undefined;
  return join(dirname(dirname(p)), ...tail, name);
}

/** The core's directory for a slice's later parts, inside the coverage directory. */
export const partsRoot = ".pipelinegen";

/** In the first part's directory: the parts the core has already taken for it. */
export const takenFile = ".taken";

/**
 * The first part's end notes which numbered parts are its; a later part's end moves every one
 * written since into its own directory. The batch closes each part before the next starts.
 */
export function takePart(dir: string, slice: string): void {
  const first = firstPartDir(dir, slice);
  if (!first) {
    const own = partsOf(dir).filter((p) => p !== dir);
    if (own.length > 0)
      writeFileSync(join(dir, takenFile), `${own.map((p) => basename(p)).join("\n")}\n`);
    return;
  }
  // No note: the first part's end found nothing there, so all of it is newer.
  let taken = new Set<string>();
  try {
    taken = new Set(readFileSync(join(first, takenFile), "utf8").split("\n").filter(Boolean));
  } catch {
    // nothing taken yet
  }
  const fresh = partsOf(first).filter((p) => p !== first && !taken.has(basename(p)));
  mkdirSync(dir, { recursive: true });
  let n = partsOf(dir).length;
  for (const p of fresh) {
    n += 1;
    while (existsSync(join(dir, String(n)))) n += 1;
    renameSync(p, join(dir, String(n)));
  }
}

export function finished(part: string): boolean {
  return existsSync(join(part, doneFile));
}

export interface BrowserScript {
  url: string;
  /** The first 16 hex digits of the SHA-256 of the script's text. */
  sha: string;
  /** [start, end) in UTF-16 units of the script's text. */
  ran: [number, number][];
  /** Starts of the functions V8 reported as run. A function it never reported never ran. */
  fns: number[];
  /** In UTF-8 bytes, to say how a build's copy differs. */
  size?: number;
}

export interface BrowserRecord {
  version: 1;
  /** Coverage needs Chromium: in another browser the record says so and observe can't tell. */
  browser: string;
  supported: boolean;
  scripts: BrowserScript[];
  /** Every URL the pages requested, without query: static files bind through these. */
  requests: string[];
  /** The pages' own documents (top-level navigations): their origins serve the app. */
  documents?: string[];
}

export interface NodeScript {
  file: string;
  /** The test runner's transform of the file. */
  source: string;
  /** Sources are paths from the tree's root. */
  map?: {
    version: number;
    sources: string[];
    sourcesContent?: (string | null)[];
    mappings: string;
    names?: string[];
  };
  ran: [number, number][];
  fns: number[];
}

export interface NodeRecord {
  version: 1;
  scripts: NodeScript[];
}

export function mapRef(source: string): string | undefined {
  const m = /\/\/[#@] sourceMappingURL=([^\s'"]+)\s*$/.exec(source.slice(-2000));
  return m?.[1];
}

function kindOf(text: string): string {
  const head = text.trimStart();
  if (head === "") return "it is empty";
  if (/^<(!doctype\s+html|html)\b/i.test(head)) return "it is an HTML document";
  if (head.startsWith("<")) return "it is markup";
  const shown = JSON.stringify(head.slice(0, 16));
  return head.startsWith("{") || head.startsWith("[")
    ? `it is not JSON a source map can be read from (it starts ${shown})`
    : `it is not JSON (it starts ${shown})`;
}

/** A server's single-page fallback answers a map it doesn't have with its page and status 200. */
export function notAMap(text: string): string | undefined {
  let m: unknown;
  try {
    m = JSON.parse(text);
  } catch {
    return kindOf(text);
  }
  if (typeof m !== "object" || m === null) return kindOf(text);
  const o = m as { mappings?: unknown; sources?: unknown; sections?: unknown };
  if (Array.isArray(o.sections)) return undefined;
  if (typeof o.mappings === "string" && Array.isArray(o.sources)) return undefined;
  return "it is JSON, but no source map: it has no mappings and sources, nor sections";
}

/** A text that passes and still can't be parsed fails where it is read, for its script alone. */
export function notAMapAtAGlance(text: string): string | undefined {
  const head = text.trimStart();
  if (!head.startsWith("{")) return kindOf(text);
  if (!text.includes('"mappings"') && !text.includes('"sections"'))
    return "it is JSON, but no source map: it has no mappings and sources, nor sections";
  return undefined;
}
