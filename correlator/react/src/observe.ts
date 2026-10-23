// Turns what a slice left behind (browser and Node coverage, the URLs its pages requested) into the
// units it touched.
import { createHash } from "node:crypto";
import { type Dirent, existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { join, posix, resolve } from "node:path";
import { isScript, scriptId, summarize } from "@sideways-works/ts-sbom";
import { type Build, findBuilds } from "./builds.ts";
import { type BundleIndex, buildIndex, indexOf, mapPathOf } from "./bundles.ts";
import { contains, type Intervals } from "./intervals.ts";
import { depId, fileId } from "./links.ts";
import {
  type BrowserRecord,
  type BrowserScript,
  browserFile,
  doneFile,
  finished,
  mapFailSuffix,
  mapRef,
  type NodeRecord,
  type NodeScript,
  nodeFile,
  notAMapAtAGlance,
  partsOf,
  scriptsDir,
} from "./record.ts";
import { packageName } from "./resolve.ts";

/** Where an app's build lives, and how its server maps URLs onto it. */
export interface BundleConfig {
  /** From the tree's root (`app/dist`). */
  dist: string;
  /** The path the app is served under, or a full URL when another origin (a CDN) serves its scripts. */
  base?: string;
  /** The page the build started from, which holds the page's inline scripts. */
  html?: string;
  /** The directory copied as is into the build (`app/public`). */
  public?: string;
}

export interface ObserveConfig {
  /** The builds, when the bundler configs in the tree don't say where they are. */
  bundles?: BundleConfig[];
  /**
   * Where the builds are when the run made them outside the tree: a directory laid out like the tree
   * (`<build_root>/app/dist/...`). Source maps still resolve against the tree.
   */
  build_root?: string;
}

/** Observe can't tell what the slice touched; the reason says why. */
export class CannotTell extends Error {}

const whereObserveRuns =
  "https://github.com/sideways-works/pipelinegen-correlator-react#where-observe-runs-and-what-it-needs-there";

function isDir(p: string): boolean {
  try {
    return statSync(p).isDirectory();
  } catch {
    return false;
  }
}

function isFile(p: string): boolean {
  try {
    return statSync(p).isFile();
  } catch {
    return false;
  }
}

/** Whether the innermost function around `g` ran (or `g` is at the script's top level). */
function innerRan(index: BundleIndex, fnIdx: number, reported: number[]): boolean {
  if (fnIdx < 0) return true;
  const start = index.fns[fnIdx * 2] as number;
  const body = index.fns[fnIdx * 2 + 1] as number;
  // V8 puts a function's start at its first token or its name; any reported start between the
  // function's first token and its body is this function.
  let lo = 0;
  let hi = reported.length;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if ((reported[mid] as number) < start) lo = mid + 1;
    else hi = mid;
  }
  return lo < reported.length && (reported[lo] as number) <= body;
}

/** `loaded`: what ran while modules loaded (their top-level code); `ran`: what ran after. */
export interface Touched {
  ran: Set<string>;
  loaded: Set<string>;
  /** Map sources of scripts that ran that the tree lacks: what ran there binds nothing. */
  missing?: Set<string>;
  /** Built scripts without a map that hold only the bundler's own code. */
  generated?: Set<string>;
  /** Built scripts that load many modules at once, by how many. */
  whole?: Map<string, number>;
  /** Which build each origin was found to serve, where several builds were configured. */
  served?: Map<string, string>;
  /** Origins no build holds a script of, outside the app's, by how many scripts ran there. */
  foreign?: Map<string, number>;
}

/** This machine or a private network: where a test serves its app. */
function isLocal(origin: string): boolean {
  const h = new URL(origin).hostname.replace(/^\[|\]$/g, "");
  return (
    h === "localhost" ||
    h.endsWith(".localhost") ||
    h === "::1" ||
    /^127\./.test(h) ||
    /^10\./.test(h) ||
    /^192\.168\./.test(h) ||
    /^172\.(1[6-9]|2[0-9]|3[01])\./.test(h)
  );
}

/** The same host, or the same last two labels of it. */
function sameSite(a: string, b: string): boolean {
  const ha = new URL(a).hostname;
  const hb = new URL(b).hostname;
  if (ha === hb) return true;
  const site = (h: string) => h.split(".").slice(-2).join(".");
  return !/^[0-9.]+$/.test(ha) && site(ha) === site(hb);
}

function basePath(app: BundleConfig): string {
  const base = app.base ?? "/";
  if (!/^https?:\/\//.test(base)) return base;
  const path = new URL(base).pathname;
  return path.endsWith("/") ? path : `${path}/`;
}

function baseOrigin(app: BundleConfig): string | undefined {
  const origin = (app as Build).origin;
  if (origin) return origin;
  return app.base && /^https?:\/\//.test(app.base) ? new URL(app.base).origin : undefined;
}

/** How many modules one script loads before observe says it loads them all at once. */
const wholeAppModules = 20;

function scriptFileOf(id: string): string | undefined {
  if (id.startsWith("*")) return id.slice(1);
  if (!id.startsWith("ts:")) return undefined;
  return id.slice(3).split("#")[0];
}

export function touchedIn(index: BundleIndex, ran: Intervals, fns: number[], out: Touched): void {
  if (ran.length === 0) return;
  const add = (id: string, to: Set<string>) => {
    for (const x of index.expand[id] ?? [id]) to.add(x);
  };
  const reported = [...fns].sort((a, b) => a - b);
  // Per module: whether some of its code ran, whether it has top-level code of its own, and the
  // generated functions that hold its code.
  const started = new Set<string>();
  const topLevel = new Set<string>();
  const holders = new Map<string, Set<number>>();
  for (let i = 0; i < index.segs.length; i += 3) {
    const unit = index.units[index.segs[i + 2] as number] as string;
    const file = scriptFileOf(unit);
    const fn = index.segs[i + 1] as number;
    if (file) {
      if (/#(init|const:|class:)/.test(unit)) topLevel.add(file);
      const h = holders.get(file) ?? new Set<number>();
      h.add(fn);
      holders.set(file, h);
    }
    const g = index.segs[i] as number;
    if (!contains(ran, g)) continue;
    if (!innerRan(index, fn, reported)) continue;
    if (file) started.add(file);
    add(unit, fn < 0 ? out.loaded : out.ran);
  }
  // Each loaded module's init counts, unless the evidence says it was never evaluated: its own
  // top-level code is mapped and none of it ran (a lazy initialiser never called), or it holds only
  // declarations whose outermost functions sit in a wrapper that never ran. When unsure it counts:
  // wider is the safe side.
  const evaluated = (file: string): boolean => {
    const held = holders.get(file);
    if (!held || started.has(file)) return true;
    if (topLevel.has(file)) return false;
    const parents = index.parents;
    if (!parents) return true;
    for (const f of held) {
      if (f < 0) return true;
      let g = f;
      while ((parents[g] ?? -1) >= 0 && held.has(parents[g] as number)) g = parents[g] as number;
      const p = parents[g] ?? -1;
      if (p < 0 || innerRan(index, p, reported)) return true;
    }
    return false;
  };
  for (const l of index.loads) {
    const file = scriptFileOf(l);
    if (!file || evaluated(file)) add(l, out.loaded);
  }
}

function sha16(text: string): string {
  return createHash("sha256").update(text).digest("hex").slice(0, 16);
}

/**
 * A part's record, if it has one. One that can't be parsed or lists no scripts is a broken
 * recording: observe fails rather than answer from part of what the slice ran.
 */
function readRecord<T extends { scripts: unknown[] }>(path: string): T | undefined {
  if (!existsSync(path)) return undefined;
  const text = readFileSync(path, "utf8");
  const broken = (what: string) =>
    `${path} (${Buffer.byteLength(text)} bytes) ${what}: the slice's recording is unusable`;
  let r: unknown;
  try {
    r = JSON.parse(text);
  } catch (e) {
    throw new SyntaxError(broken(`is not JSON (${(e as Error).message})`));
  }
  if (typeof r !== "object" || r === null || !Array.isArray((r as T).scripts))
    throw new Error(broken("lists no scripts, so the fixture didn't write it whole"));
  return r as T;
}

/** `dirs[0]` is the setup's directory, the rest are the slice's. Every finished part counts. */
export function observe(
  root: string,
  dirs: string[],
  config: ObserveConfig = {},
  cache?: string,
  claimed?: string[],
): {
  touched: string[];
  setup_like: string[];
  missing: string[];
  generated: string[];
  whole: string[];
  served: string[];
  foreign: string[];
} {
  const touched: Touched = {
    ran: new Set(),
    loaded: new Set(),
    missing: new Set(),
    generated: new Set(),
    whole: new Map(),
    served: new Map(),
    foreign: new Map(),
  };
  dirs.forEach((d, i) => {
    // What ran before the batch's first test is setup, whatever ran it.
    const setup = i === 0;
    const into: Touched = setup
      ? {
          ran: touched.loaded,
          loaded: touched.loaded,
          missing: touched.missing,
          generated: touched.generated,
          whole: touched.whole,
          served: touched.served,
          foreign: touched.foreign,
        }
      : touched;
    const parts = partsOf(d);
    // A missing or empty directory is what a run without the fixture leaves, and a part without its
    // marker was cut off while being written. A setup that recorded nothing is fine.
    if (!setup) {
      if (!existsSync(d)) throw new CannotTell(`${d} is missing: the slice left no recording here`);
      if (readdirSync(d).length === 0)
        throw new CannotTell(`${d} is empty: nothing recorded here (is the fixture installed?)`);
      if (parts.length === 0)
        throw new CannotTell(
          `${d} holds no recording the fixture wrote (no part with a ${doneFile} file)`,
        );
    }
    for (const part of parts) {
      if (!finished(part))
        throw new CannotTell(`${part} holds an unfinished recording (no ${doneFile} file)`);
      const b = readRecord<BrowserRecord>(join(part, browserFile));
      if (b) {
        if (!b.supported)
          throw new CannotTell(`coverage needs Chromium; the slice ran in ${b.browser}`);
        observeBrowser(root, b, config, cache, claimed, into, join(dirs[0] as string, scriptsDir));
      }
      const n = readRecord<NodeRecord>(join(part, nodeFile));
      if (n) {
        for (const s of n.scripts) observeNode(root, s, into);
      }
    }
  });
  const all = new Set([...touched.ran, ...touched.loaded]);
  return {
    touched: [...all].sort(),
    // Run only while modules loaded: module init and the data built then.
    setup_like: [...touched.loaded].filter((u) => !touched.ran.has(u)).sort(),
    missing: [...(touched.missing ?? [])].sort(),
    generated: [...(touched.generated ?? [])].sort(),
    whole: [...(touched.whole ?? new Map()).entries()]
      .sort()
      .map(([p, n]) => `${p} (${n} modules)`),
    served: [...(touched.served ?? new Map()).entries()].sort().map(([o, d]) => `${o} serves ${d}`),
    foreign: [...(touched.foreign ?? new Map()).entries()].sort().map(([o, n]) => `${o} (${n})`),
  };
}

function unknownNote(bundles: BundleConfig[]): string {
  const xs = bundles.flatMap((b) =>
    ((b as Build).unknown ?? []).map((u) => `${(b as Build).config}: ${u}`),
  );
  return xs.length
    ? `; settings computed at run time, which the tree can't tell: ${xs.join(", ")}`
    : "";
}

/** Only for a request without claims, which has no other way to find the bundler configs. */
function walk(root: string, dir: string, out: string[]): string[] {
  let entries: Dirent[];
  try {
    entries = readdirSync(join(root, dir), { withFileTypes: true });
  } catch {
    return out;
  }
  for (const e of entries) {
    const rel = dir ? `${dir}/${e.name}` : e.name;
    if (e.isDirectory()) {
      if (e.name !== "node_modules" && !e.name.startsWith(".")) walk(root, rel, out);
    } else if (e.isFile()) out.push(rel);
  }
  return out;
}

const walked = new Map<string, Build[]>();
/** The builds the bundler configs among the claimed files describe (or, without claims, the tree's). */
function detected(root: string, claimed: string[] | undefined): Build[] {
  if (claimed) return findBuilds(root, claimed.slice().sort());
  let b = walked.get(root);
  if (!b) {
    b = findBuilds(root, walk(root, "", []).sort());
    walked.set(root, b);
  }
  return b;
}

interface Saved {
  js: string;
  map?: string;
}

/**
 * The scripts a recording kept, by sha. One whose map was named but not kept is left out: read as
 * having no map, it could be taken for the bundler's own code.
 */
function savedScripts(dir: string): Map<string, Saved> {
  const out = new Map<string, Saved>();
  let names: string[];
  try {
    names = readdirSync(dir);
  } catch {
    return out;
  }
  const have = new Set(names);
  for (const n of names) {
    if (!n.endsWith(".js")) continue;
    const sha = n.slice(0, -3);
    if (have.has(`${sha}.map`)) out.set(sha, { js: join(dir, n), map: join(dir, `${sha}.map`) });
    else if (have.has(`${sha}.nomap`)) out.set(sha, { js: join(dir, n) });
  }
  return out;
}

/** Apps served alike (`/assets/...` on each port) are told apart by where their sources are. */
function bestByMap(
  root: string,
  candidates: { app: BundleConfig; path: string }[],
  saved: Saved,
): { app: BundleConfig; path: string } | undefined {
  if (!saved.map || candidates.length < 2) return undefined;
  let sources: string[];
  let sourceRoot = "";
  try {
    const m = JSON.parse(readFileSync(saved.map, "utf8")) as {
      sources?: string[];
      sourceRoot?: string;
    };
    sources = (m.sources ?? []).filter((x) => !x.includes("node_modules/") && !x.includes("\0"));
    sourceRoot = m.sourceRoot ?? "";
  } catch {
    return undefined;
  }
  let best: { app: BundleConfig; path: string } | undefined;
  let most = 0;
  for (const c of candidates) {
    const base = posix.dirname(c.path);
    const n = sources.filter((x) =>
      isFile(join(root, posix.normalize(posix.join(base, sourceRoot, x)))),
    ).length;
    if (n > most) {
      most = n;
      best = c;
    }
  }
  return best;
}

const generated = "";

/**
 * What a built script without a source map is, told by evidence:
 *
 * - the public directory's file of the same path: "" here, as it binds as its file through the
 *   page's requests;
 * - a byte-for-byte copy of a file of the tree (an asset imported by URL): that file's unit;
 * - a copy of a file of an installed package (a library's worker): the package's unit;
 * - with source maps for the build's other scripts, code the bundler wrote itself: a bundler writes
 *   a map for every chunk that holds a module of the app, so it binds nothing;
 * - otherwise undefined: the build has no source maps at all.
 */
export function withoutMap(
  root: string,
  app: BundleConfig,
  path: string,
  js: string,
  built: (p: string) => string,
  builds: BundleConfig[] = [app],
  keptMaps?: boolean,
): string | undefined {
  const pub = app.public ?? posix.join(posix.dirname(app.dist), "public");
  const rel = posix.relative(app.dist, path);
  if (!rel.startsWith("..") && isFile(join(root, pub, rel))) return generated;
  // A build's own output is no source a script was copied from.
  const outputs = builds.map((b) => `${b.dist}/`);
  const copy = copyOf(root, posix.basename(path), js, (p) => outputs.some((o) => p.startsWith(o)));
  if (copy !== undefined) {
    const nm = copy.lastIndexOf("node_modules/");
    return nm >= 0 ? depId(packageName(copy.slice(nm + 13))) : fileId(copy);
  }
  return keptMaps || buildHasMaps(built(app.dist)) ? generated : undefined;
}

/** Bundlers name a copied file `<name>-<hash><ext>`: its name and extension. */
export function unhashed(base: string): string {
  const m = /^(.+)-[A-Za-z0-9_-]{8}(\.[^.]+)$/.exec(base);
  return m ? `${m[1]}${m[2]}` : base;
}

const filesByName = new Map<string, Map<string, string[]>>();

function byName(root: string): Map<string, string[]> {
  let m = filesByName.get(root);
  if (m) return m;
  m = new Map();
  const visit = (dir: string) => {
    let entries: Dirent[];
    try {
      entries = readdirSync(join(root, dir), { withFileTypes: true });
    } catch {
      return;
    }
    for (const e of entries) {
      const rel = dir ? `${dir}/${e.name}` : e.name;
      if (e.isDirectory()) {
        // .pnpm holds a pnpm install's packages; other hidden directories are tools' state.
        if (e.name !== ".git" && (!e.name.startsWith(".") || e.name === ".pnpm")) visit(rel);
      } else if (e.isFile()) {
        const list = m?.get(e.name) ?? [];
        list.push(rel);
        m?.set(e.name, list);
      }
    }
  };
  visit("");
  filesByName.set(root, m);
  return m;
}

function copyOf(
  root: string,
  base: string,
  js: string,
  skip: (p: string) => boolean,
): string | undefined {
  for (const p of (byName(root).get(unhashed(base)) ?? []).sort()) {
    if (skip(p)) continue;
    try {
      if (readFileSync(join(root, p), "utf8") === js) return p;
    } catch {
      // unreadable: not a copy
    }
  }
  return undefined;
}

function buildHasMaps(dir: string): boolean {
  const walk = (d: string): boolean => {
    let entries: Dirent[];
    try {
      entries = readdirSync(d, { withFileTypes: true });
    } catch {
      return false;
    }
    return entries.some((e) =>
      e.isDirectory() ? walk(join(d, e.name)) : /\.(c|m)?js\.map$/.test(e.name),
    );
  };
  return walk(dir);
}

function observeBrowser(
  root: string,
  rec: BrowserRecord,
  config: ObserveConfig,
  cache: string | undefined,
  claimed: string[] | undefined,
  out: Touched,
  store: string,
): void {
  const bundles = config.bundles ?? detected(root, claimed);
  const appOf = new Map<string, BundleConfig>(); // origin → app whose build served it
  const outside = config.build_root ? resolve(config.build_root) : undefined;
  const kept = savedScripts(store);
  // A kept map that is no source map (a server's page, a file cut short) reads as a script kept
  // without its map, which could otherwise pass for the bundler's own code.
  const badMaps = new Map<string, string>();
  for (const s of rec.scripts) {
    const sv = kept.get(s.sha);
    if (!sv?.map || badMaps.has(s.sha)) continue;
    let why: string | undefined;
    try {
      why = notAMapAtAGlance(readFileSync(sv.map, "utf8"));
    } catch (e) {
      why = (e as Error).message;
    }
    if (!why) continue;
    let ref: string | undefined;
    try {
      ref = mapRef(readFileSync(sv.js, "utf8"));
    } catch {
      // named as the script's URL with .map
    }
    let url = ref ?? `${s.url}.map`;
    try {
      url = new URL(url, s.url).href;
    } catch {
      // a script whose URL is no base
    }
    badMaps.set(
      s.sha,
      `the map it names, ${url}, which the recording kept, is no source map: ${why}`,
    );
    kept.delete(s.sha);
  }
  const noMapWhy = (sha: string): string => {
    const bad = badMaps.get(sha);
    if (bad) return bad;
    try {
      return readFileSync(join(store, `${sha}${mapFailSuffix}`), "utf8").trim();
    } catch {
      return "the fixture recorded no reason (a recording made before it did)";
    }
  };
  const keptNoMap = new Set(
    (() => {
      try {
        return readdirSync(store)
          .filter((n) => n.endsWith(".js") && !kept.has(n.slice(0, -3)))
          .map((n) => n.slice(0, -3));
      } catch {
        return [];
      }
    })(),
  );
  // Chromium reported coverage without the script's text: the fixture records the sha of nothing.
  const textless = (s: BrowserScript) => s.sha === sha16("") && !s.size;
  const noText =
    "the recording kept no copy of it: the browser's coverage came without the script's text";
  const differs = (path: string, s: BrowserScript, js: string): string => {
    const file = Buffer.from(js);
    const facts = `browser sha ${s.sha}${s.size === undefined ? "" : `, ${s.size} bytes`}; file sha ${sha16(js)}, ${file.length} bytes`;
    if (textless(s))
      return `${path} in the build can't be checked against the script the browser ran (${s.url}): ${noText}`;
    if (!keptNoMap.has(s.sha))
      return `${path} in the build differs from the script the browser ran (${s.url}), and the recording kept no copy of it (${facts}): the build observe reads is not the one the specs were served, or a step changed the file after the build`;
    const ran = readFileSync(join(store, `${s.sha}.js`));
    let at = 0;
    while (at < ran.length && at < file.length && ran[at] === file[at]) at++;
    return `${path} in the build differs from the script the browser ran (${s.url}; ${facts}; first difference at byte ${at}); the recording kept that script but not its source map: ${noMapWhy(s.sha)}`;
  };
  // A build_root that isn't here: the observing step wasn't given the built files. The scripts the
  // recording kept make it needless.
  if (config.build_root && outside && !isDir(outside) && kept.size === 0)
    throw new CannotTell(
      `build_root ${config.build_root} does not exist here (${process.cwd()}): the step that observes must be given the built files: see ${whereObserveRuns}`,
    );
  // A build's directory too, so whether the build has maps is asked of the build that was served.
  const built = (p: string) => {
    if (outside && existsSync(join(outside, p))) return join(outside, p);
    return join(root, p);
  };
  const candidatesOf = (u: URL): { app: BundleConfig; path: string }[] => {
    const candidates: { app: BundleConfig; path: string }[] = [];
    for (const app of bundles) {
      const base = basePath(app);
      if (!u.pathname.startsWith(base)) continue;
      const rel = decodeURIComponent(u.pathname.slice(base.length));
      if (rel) candidates.push({ app, path: posix.join(app.dist, rel) });
    }
    return candidates;
  };
  const inBuild = (
    u: URL,
    sha: string,
  ): { app: BundleConfig; path: string; saved?: Saved } | undefined => {
    const candidates = candidatesOf(u);
    const saved = kept.get(sha);
    // A file several builds hold alike (a bundler's runtime chunk) goes to the build of the app the
    // origin serves, else the first.
    const held = candidates.filter((c) => isFile(built(c.path)));
    const onDisk = held.find((c) => c.app === servedBy.get(u.origin)) ?? held[0];
    if (!saved) return onDisk;
    // A kept script belongs to the app whose build holds it, else the one its map's sources resolve
    // in, else the one the origin's kept maps resolve in.
    const told = mappedApps.get(u.origin);
    const byOrigin = told?.size === 1 ? candidates.find((c) => told.has(c.app)) : undefined;
    const pick = onDisk ?? bestByMap(root, candidates, saved) ?? byOrigin ?? candidates[0];
    return pick ? { ...pick, saved } : undefined;
  };
  // Where an origin served a kept script with its map, a kept script without one is the bundler's
  // own code; an app that served none has no maps.
  const mappedOrigins = new Set<string>();
  const mappedApps = new Map<string, Set<BundleConfig>>();
  for (const s of rec.scripts) {
    try {
      const saved = kept.get(s.sha);
      if (!saved?.map) continue;
      const u = new URL(s.url);
      mappedOrigins.add(u.origin);
      const candidates = candidatesOf(u);
      const app =
        candidates.length === 1 ? candidates[0]?.app : bestByMap(root, candidates, saved)?.app;
      if (!app) continue;
      const apps = mappedApps.get(u.origin) ?? new Set();
      apps.add(app);
      mappedApps.set(u.origin, apps);
    } catch {
      // not a URL
    }
  }
  const servedBy = new Map<string, BundleConfig>();
  const served = new Set<string>();
  for (const s of rec.scripts) {
    let u: URL;
    try {
      u = new URL(s.url);
    } catch {
      continue;
    }
    const holders = bundles.filter((app) => {
      const base = basePath(app);
      const rel = u.pathname.startsWith(base)
        ? decodeURIComponent(u.pathname.slice(base.length))
        : "";
      return rel !== "" && isFile(built(posix.join(app.dist, rel)));
    });
    if (holders.length > 0) served.add(u.origin);
    if (holders.length === 1 && !servedBy.has(u.origin))
      servedBy.set(u.origin, holders[0] as BundleConfig);
  }
  // The origins the app's build is served from: one a build holds a file of at its path, one that
  // served a kept script with its map, a full URL base, this machine's origins and those on the same
  // site. A script of any other origin is a third party's and binds nothing. Where no script tells,
  // every origin may be the app's.
  const told = [...served, ...mappedOrigins];
  const named = bundles.map(baseOrigin).filter((o): o is string => o !== undefined);
  const appsOrigin = (o: string): boolean =>
    told.length === 0 ||
    told.includes(o) ||
    named.includes(o) ||
    isLocal(o) ||
    [...told, ...named].some((h) => sameSite(h, o));
  if (bundles.length > 1) for (const [o, app] of servedBy) out.served?.set(o, app.dist);
  const docs = new Set(rec.documents ?? []);
  const pages: URL[] = [];
  const unknown: BrowserScript[] = [];
  for (const s of rec.scripts) {
    let u: URL;
    try {
      u = new URL(s.url);
    } catch {
      continue;
    }
    const hit = inBuild(u, s.sha);
    if (!hit) {
      if (docs.has(`${u.origin}${u.pathname}`) || !rec.documents) pages.push(u);
      else unknown.push(s);
      continue;
    }
    // A kept script is placed in a build by its path alone: one of another origin is no file of it.
    if ((u.protocol === "http:" || u.protocol === "https:") && !appsOrigin(u.origin)) {
      out.foreign?.set(u.origin, (out.foreign.get(u.origin) ?? 0) + 1);
      continue;
    }
    // A script at a page's own URL, where no file of the build is, is that page's inline script.
    if (docs.has(`${u.origin}${u.pathname}`) && !isFile(built(hit.path))) {
      appOf.set(u.origin, hit.app);
      pages.push(u);
      continue;
    }
    appOf.set(u.origin, hit.app);
    const sv = hit.saved;
    const keptMap = sv?.map ? mapPathOf(hit.path, readFileSync(sv.js, "utf8")) : undefined;
    const read = (p: string) => {
      if (sv && p === hit.path) return sv.js;
      if (sv?.map && p === keptMap) return sv.map;
      return built(p);
    };
    const js = readFileSync(read(hit.path), "utf8");
    if (sha16(js) !== s.sha) throw new CannotTell(differs(hit.path, s, js));
    // A map that reads as one at a glance and still can't be read is this script's trouble, not the
    // end of every slice's answer.
    let index: BundleIndex | undefined;
    try {
      index = indexOf(root, hit.path, cache, read);
    } catch (e) {
      throw new CannotTell(
        `${hit.path} (${s.url}): its source map (${keptMap ? "the one the recording kept" : mapPathOf(hit.path, js)}) can't be read: ${(e as Error).message}`,
      );
    }
    if (!index) {
      const unit = withoutMap(
        root,
        hit.app,
        hit.path,
        js,
        built,
        bundles,
        sv ? mappedOrigins.has(u.origin) : undefined,
      );
      if (unit === undefined)
        throw new CannotTell(
          `the app's build has no source map for ${hit.path}: build with source maps for recording runs (Vite: build.sourcemap: true)`,
        );
      if (unit === generated) out.generated?.add(hit.path);
      else if (unit) out.loaded.add(unit);
      continue;
    }
    noteMissing(index, out);
    // Every module of one script initialises when it loads, for each spec that loads it.
    const inits = index.loads.filter((l) => l.startsWith("ts:")).length;
    if (inits >= wholeAppModules) out.whole?.set(hit.path, inits);
    touchedIn(index, s.ran, s.fns, out);
  }
  // A script on an app origin that no build holds can't be named. A page of a third party the test
  // opened (a payment provider's window) is no app origin: what it ran binds nothing and is named.
  const held = [...appOf.keys()];
  const appOrigins = new Set(held);
  for (const d of docs) {
    const o = new URL(d).origin;
    if (held.length === 0 || isLocal(o) || held.some((h) => sameSite(h, o))) appOrigins.add(o);
  }
  const lost = unknown.filter((s) => appOrigins.has(new URL(s.url).origin));
  for (const s of unknown) {
    const o = new URL(s.url).origin;
    if (!appOrigins.has(o)) out.foreign?.set(o, (out.foreign.get(o) ?? 0) + 1);
  }
  const notKept = (s: BrowserScript): string => {
    if (keptNoMap.has(s.sha))
      return `the recording kept it without its source map: ${noMapWhy(s.sha)}`;
    if (kept.has(s.sha)) return "the recording kept it, but no build's base serves its path";
    if (textless(s)) return noText;
    return `the recording kept no copy of it (browser sha ${s.sha}${s.size === undefined ? "" : `, ${s.size} bytes`})`;
  };
  const first = lost[0];
  if (first)
    throw new CannotTell(
      `${first.url} ran on the app's origin, but no build in the tree holds it (builds: ${
        bundles
          .map((b) =>
            outside && isDir(join(outside, b.dist))
              ? `${b.dist} in ${config.build_root}`
              : isDir(join(root, b.dist))
                ? b.dist
                : `expected at ${b.dist} (not found)`,
          )
          .join(", ") || "none found"
      }${unknownNote(bundles)}); ${notKept(first)}`,
    );
  // A page's inline scripts come from the page the build started from.
  for (const u of pages) {
    const app = appOf.get(u.origin);
    if (
      app?.html &&
      isFile(join(root, app.html)) &&
      rec.scripts.some((s) => s.url.startsWith(u.origin))
    )
      out.loaded.add(fileId(app.html));
  }
  for (const r of rec.requests ?? []) {
    let u: URL;
    try {
      u = new URL(r);
    } catch {
      continue;
    }
    const app = appOf.get(u.origin);
    if (!app) continue;
    const pub = app.public ?? posix.join(posix.dirname(app.dist), "public");
    const base = basePath(app);
    if (!u.pathname.startsWith(base)) continue;
    const p = posix.join(pub, decodeURIComponent(u.pathname.slice(base.length)));
    if (p.startsWith(pub) && isFile(join(root, p))) out.ran.add(fileId(p));
  }
}

function observeNode(root: string, s: NodeScript, out: Touched): void {
  if (!isScript(s.file)) return;
  const read = (p: string): string | undefined => {
    try {
      return readFileSync(join(root, p), "utf8");
    } catch {
      return undefined;
    }
  };
  if (s.map) {
    // The map names the tree's sources, whether or not the script itself is in the tree.
    let index: BundleIndex;
    try {
      index = buildIndex("_", s.source, s.map, read);
    } catch (e) {
      throw new CannotTell(
        `${s.file} ran in Node with a source map that can't be read: ${(e as Error).message}`,
      );
    }
    noteMissing(index, out);
    touchedIn(index, s.ran, s.fns, out);
    return;
  }
  const text = read(s.file);
  if (text === undefined)
    // The fixture records only the tree's own files, so a file the tree lacks, with no map, means the
    // recording is of another tree: what ran can't be named.
    throw new CannotTell(
      `${s.file} ran in Node, but the tree has no such file and the record no source map`,
    );
  if (s.source !== text) throw new CannotTell(`${s.file} ran transformed and without a source map`);
  // The file ran as written: its own offsets are the tree's.
  const sum = summarize(s.file, text);
  const reported = [...s.fns].sort((a, b) => a - b);
  for (const u of sum.units) {
    if (u.kind === "type") continue;
    const ran =
      u.key === "init"
        ? s.ran.length > 0
        : u.ranges.some(
            ([a, b]) =>
              reported.some((f) => f >= a && f < b) && s.ran.some(([x, y]) => x < b && y > a),
          );
    if (ran) (u.key === "init" ? out.loaded : out.ran).add(scriptId(s.file, u.key));
  }
}

function noteMissing(index: BundleIndex, out: Touched): void {
  for (const m of index.missing ?? []) out.missing?.add(m);
}
