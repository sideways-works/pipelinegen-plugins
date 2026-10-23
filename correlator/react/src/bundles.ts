// A built script and its source map, turned into an index from offsets in the built code to the
// tree's units, cached by content.
import { createHash } from "node:crypto";
import { mkdirSync, readFileSync, renameSync, writeFileSync } from "node:fs";
import { join, posix } from "node:path";
import {
  type FileSummary,
  isScript,
  isStyle,
  scriptId,
  styleId,
  summarize,
} from "@sideways-works/ts-sbom";
import { SourceMapConsumer } from "source-map-js";
import ts from "typescript";
import { depId, fileId } from "./links.ts";
import { packageName } from "./resolve.ts";

export const indexVersion = "5";

export interface RawMap {
  version: number;
  sources: string[];
  sourcesContent?: (string | null)[];
  sourceRoot?: string;
  mappings: string;
  names?: string[];
}

export interface BundleIndex {
  version: string;
  /** Flat triples: generated offset, index into `fns` (-1 at top level), index into `units` (-1 for none). */
  segs: number[];
  /** Flat pairs: a function's start and the start of its body. */
  fns: number[];
  /** Each function's enclosing function, by index into `fns` (-1 at top level). */
  parents?: number[];
  units: string[];
  /** What loading the script runs: every source module's init, packages, data files. */
  loads: string[];
  /** Script sources a build plugin changed before bundling: positions follow the map's text. */
  stale: string[];
  /** Stand-ins for every unit of a file, where a position names no unit of the tree. */
  expand: Record<string, string[]>;
  /** Sources the map names inside the tree, outside installed packages, that the tree lacks. */
  missing?: string[];
}

export function wholeFile(path: string): string {
  return `*${path}`;
}

export function lineStarts(text: string): number[] {
  const s = [0];
  for (let i = 0; i < text.length; i++) if (text.charCodeAt(i) === 10) s.push(i + 1);
  return s;
}

/**
 * The inert ranges are where a bundler writes names the map ties to a declaration but nothing runs:
 * a hoisted `var COLORS;`, a name before its value, an export list.
 */
export function functionsOf(
  path: string,
  text: string,
): { fns: number[]; ends: number[]; inert: number[] } {
  const sf = ts.createSourceFile(path, text, ts.ScriptTarget.Latest, false, ts.ScriptKind.JS);
  const fns: number[] = [];
  const ends: number[] = [];
  const inert: number[] = [];
  const visit = (n: ts.Node): void => {
    if (ts.isFunctionLike(n) && "body" in n && n.body) {
      fns.push(n.getStart(sf), (n.body as ts.Node).getStart(sf));
      ends.push(n.end);
    } else if (ts.isClassLike(n)) {
      fns.push(n.getStart(sf), n.members.pos);
      ends.push(n.end);
    } else if (ts.isVariableStatement(n)) {
      // `var` itself: what follows has its own ranges.
      inert.push(n.getStart(sf), n.declarationList.declarations[0]?.getStart(sf) ?? n.end);
    } else if (ts.isVariableDeclaration(n)) {
      inert.push(n.getStart(sf), n.initializer ? n.initializer.getStart(sf) : n.end);
    } else if (ts.isPropertyAssignment(n)) {
      inert.push(n.getStart(sf), n.initializer.getStart(sf));
    } else if (ts.isExportDeclaration(n)) {
      inert.push(n.getStart(sf), n.end);
    }
    ts.forEachChild(n, visit);
  };
  visit(sf);
  return { fns, ends, inert };
}

function within(pairs: number[], offset: number): boolean {
  let lo = 0;
  let hi = pairs.length / 2;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if ((pairs[mid * 2 + 1] as number) <= offset) lo = mid + 1;
    else hi = mid;
  }
  return lo < pairs.length / 2 && (pairs[lo * 2] as number) <= offset;
}

function merged(pairs: number[]): number[] {
  const xs: [number, number][] = [];
  for (let i = 0; i < pairs.length; i += 2) xs.push([pairs[i] as number, pairs[i + 1] as number]);
  xs.sort((a, b) => a[0] - b[0]);
  const out: number[] = [];
  for (const [a, b] of xs) {
    const n = out.length;
    if (n && a <= (out[n - 1] as number)) out[n - 1] = Math.max(out[n - 1] as number, b);
    else out.push(a, b);
  }
  return out;
}

interface SourceInfo {
  units: string[]; // unit ids, parallel to ranges
  ranges: [number, number, number][]; // start, end, index into units
  init?: string;
  starts: number[];
  other?: string; // a non-script source: its file, style or package unit
}

/** `read` gives a tree file's text, or undefined when the tree has no such file. */
export function buildIndex(
  jsPath: string,
  js: string,
  map: RawMap,
  read: (path: string) => string | undefined,
): BundleIndex {
  const units: string[] = [];
  const unitIdx = new Map<string, number>();
  const idx = (id: string) => {
    let i = unitIdx.get(id);
    if (i === undefined) {
      i = units.length;
      units.push(id);
      unitIdx.set(id, i);
    }
    return i;
  };
  const stale: string[] = [];
  const missing: string[] = [];
  const expand: Record<string, string[]> = {};
  const loads = new Set<string>();
  const sources = new Map<string, SourceInfo | null>();
  const base = posix.dirname(jsPath);

  const repoPath = (source: string) =>
    posix.normalize(posix.join(base, map.sourceRoot ?? "", source));
  const info = (source: string): SourceInfo | null => {
    if (sources.has(source)) return sources.get(source) ?? null;
    let r: SourceInfo | null = null;
    const p = repoPath(source);
    const nm = p.lastIndexOf("node_modules/");
    if (nm >= 0)
      r = { units: [], ranges: [], starts: [], other: depId(packageName(p.slice(nm + 13))) };
    else if (!p.startsWith("..") && !p.includes("\0")) {
      const text = read(p);
      if (text !== undefined) {
        const i = map.sources.indexOf(source);
        const content = map.sourcesContent?.[i];
        if (isScript(p)) {
          // The map's positions are in the text it was made from: a plugin may have changed the file.
          const mapped = content ?? text;
          const tree = summarize(p, text);
          const known = new Set(
            tree.units.filter((u) => u.kind !== "type").map((u) => scriptId(p, u.key)),
          );
          const s: FileSummary = mapped === text ? tree : summarize(p, mapped);
          if (mapped !== text) {
            stale.push(p);
            expand[wholeFile(p)] = [...known].sort();
          }
          const ids: string[] = [];
          const ranges: [number, number, number][] = [];
          for (const u of s.units) {
            if (u.kind === "type") continue;
            const id = scriptId(p, u.key);
            // A unit the tree doesn't have under that name: the whole file, to be safe.
            ids.push(known.has(id) ? id : wholeFile(p));
            for (const [a, b] of u.ranges) ranges.push([a, b, ids.length - 1]);
          }
          const init = scriptId(p, "init");
          r = {
            units: ids,
            ranges,
            starts: lineStarts(mapped),
            init: known.has(init)
              ? init
              : s.units.some((u) => u.key === "init")
                ? wholeFile(p)
                : undefined,
          };
        } else
          r = { units: [], ranges: [], starts: [], other: isStyle(p) ? styleId(p) : fileId(p) };
      } else missing.push(p);
    }
    sources.set(source, r);
    if (r?.init) loads.add(r.init);
    else if (r?.other) loads.add(r.other);
    return r;
  };
  for (const s of map.sources) info(s);

  const { fns, ends, inert: rawInert } = functionsOf(jsPath, js);
  const inert = merged(rawInert);
  const starts = lineStarts(js);
  const consumer = new SourceMapConsumer(map as never);
  const pending: { g: number; source: string; line: number; col: number }[] = [];
  consumer.eachMapping((m) => {
    if (!m.source || m.originalLine == null) return;
    // A name the bundler wrote where nothing runs: no evidence of the code.
    if (within(inert, (starts[m.generatedLine - 1] ?? 0) + m.generatedColumn)) return;
    // Code the bundler wrote for a module as a whole maps to the module's first position, where its
    // first declaration also starts: no evidence that declaration ran.
    if (m.originalLine === 1 && m.originalColumn === 0) return;
    pending.push({
      g: (starts[m.generatedLine - 1] ?? 0) + m.generatedColumn,
      source: m.source,
      line: m.originalLine,
      col: m.originalColumn ?? 0,
    });
  });
  pending.sort((a, b) => a.g - b.g);
  const order: number[] = [];
  for (let i = 0; i < fns.length; i += 2) order.push(i / 2);
  order.sort(
    (a, b) =>
      (fns[a * 2] as number) - (fns[b * 2] as number) || (ends[b] as number) - (ends[a] as number),
  );
  const parents: number[] = new Array(fns.length / 2).fill(-1);
  {
    const open: number[] = [];
    for (const f of order) {
      while (
        open.length &&
        (ends[open[open.length - 1] as number] as number) <= (fns[f * 2] as number)
      )
        open.pop();
      parents[f] = open.length ? (open[open.length - 1] as number) : -1;
      open.push(f);
    }
  }
  const stack: number[] = [];
  let next = 0;
  const flat: number[] = [];
  for (const p of pending) {
    while (next < order.length && (fns[(order[next] as number) * 2] as number) <= p.g) {
      const f = order[next++] as number;
      while (
        stack.length &&
        (ends[stack[stack.length - 1] as number] as number) <= (fns[f * 2] as number)
      )
        stack.pop();
      stack.push(f);
    }
    while (stack.length && (ends[stack[stack.length - 1] as number] as number) <= p.g) stack.pop();
    const inner = stack.length ? (stack[stack.length - 1] as number) : -1;
    const si = info(p.source);
    let u = -1;
    if (si?.other) u = idx(si.other);
    else if (si) {
      const off = (si.starts[p.line - 1] ?? 0) + p.col;
      let best = -1;
      let bestLen = Number.POSITIVE_INFINITY;
      for (const [a, b, k] of si.ranges)
        if (a <= off && off < b && b - a < bestLen) {
          best = k;
          bestLen = b - a;
        }
      const id = best >= 0 ? si.units[best] : si.init;
      if (id) u = idx(id);
    }
    if (u >= 0) flat.push(p.g, inner, u);
  }
  return {
    version: indexVersion,
    segs: flat,
    fns,
    parents,
    units,
    loads: [...loads].sort(),
    stale,
    expand,
    missing: missing.sort(),
  };
}

export function indexOf(
  root: string,
  jsPath: string,
  cache?: string,
  /** Where a build file really is, when the build was made outside the tree. */
  built: (p: string) => string = (p) => join(root, p),
): BundleIndex | undefined {
  let js: string;
  let mapText: string;
  try {
    js = readFileSync(built(jsPath), "utf8");
  } catch {
    return undefined;
  }
  const mapPath = mapPathOf(jsPath, js);
  try {
    mapText = readFileSync(built(mapPath), "utf8");
  } catch {
    return undefined;
  }
  const map = JSON.parse(mapText) as RawMap;
  const read = (p: string) => {
    try {
      return readFileSync(join(root, p), "utf8");
    } catch {
      return undefined;
    }
  };
  const h = createHash("sha256").update(`${indexVersion}\0${jsPath}\0${js}\0${mapText}`);
  const base = posix.dirname(jsPath);
  for (const s of map.sources)
    h.update(`\0${read(posix.normalize(posix.join(base, map.sourceRoot ?? "", s))) ?? ""}`);
  const key = h.digest("hex");
  const file = cache ? join(cache, "bundles", `${key}.json`) : "";
  if (cache) {
    try {
      return JSON.parse(readFileSync(file, "utf8")) as BundleIndex;
    } catch {
      // not cached yet
    }
  }
  const index = buildIndex(jsPath, js, map, read);
  if (cache) {
    // The cache only saves time.
    try {
      mkdirSync(join(cache, "bundles"), { recursive: true });
      const tmp = `${file}.${process.pid}.tmp`;
      writeFileSync(tmp, JSON.stringify(index));
      renameSync(tmp, file);
    } catch {
      // the index is built again next time
    }
  }
  return index;
}

/** The map a built script names in its last line, else `<script>.map`. */
export function mapPathOf(jsPath: string, js: string): string {
  const m = /\/\/[#@] sourceMappingURL=([^\s'"]+)\s*$/.exec(js.slice(-500));
  const url = m?.[1];
  if (url && !url.startsWith("data:") && !/^[a-z]+:/i.test(url))
    return posix.normalize(posix.join(posix.dirname(jsPath), url));
  return `${jsPath}.map`;
}
