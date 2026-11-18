// The collector: a Playwright fixture that records, per spec file, the browser and Node code it
// ran. It lives in the test process because Chromium keeps a page's coverage in the page's own
// debugging session, which ends when the page closes, and Node's coverage of the test worker can
// only be read from inside that worker.
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, renameSync, writeFileSync } from "node:fs";
import { Session } from "node:inspector/promises";
import { dirname, isAbsolute, join, posix, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";
import type { Browser, BrowserContext, Page, TestInfo, TestType } from "@playwright/test";
import { type FunctionCoverage, type Intervals, ranIntervals, union } from "./intervals.ts";
import { markStart, markStop, marksDir } from "./marks.ts";
import {
  type BrowserRecord,
  type BrowserScript,
  browserFile,
  doneFile,
  mapFailSuffix,
  mapRef,
  type NodeRecord,
  type NodeScript,
  newPart,
  nodeFile,
  notAMap,
  scriptsDir,
} from "./record.ts";

export interface CoverageOptions {
  /**
   * Where to write: `<out>/<spec file>/<name>/` and `<out>/_setup/<name>/`, each run a numbered part
   * inside. Unset records nothing.
   */
  out?: string;
  /** Spec files and scripts are named by their path from it. */
  root: string;
  /** The directory name inside each slice's directory. */
  name?: string;
  /** Also record the Node side: the spec files and the helpers they import. */
  node?: boolean;
  /** Called after a spec file's directory is written, before the next file starts. */
  onSliceEnd?: (slice: string, dir: string) => void | Promise<void>;
  /** Called before a spec file's first test. */
  onSliceStart?: (slice: string, dir: string) => void | Promise<void>;
  /** Called once, after what ran before the first test is written to `_setup`. */
  onSetupEnd?: (dir: string) => void | Promise<void>;
}

interface Acc {
  sha: string;
  size: number;
  ran: Intervals;
  fns: Set<number>;
}

function sha16(text: string): string {
  return createHash("sha256").update(text).digest("hex").slice(0, 16);
}

export { mapRef };

/** A map library's map runs to megabytes. */
export const mapFetchSeconds = 60;

/** A server's single-page fallback answers a URL it has no file for with its page and status 200. */
function notAMapAnswer(status: number, type: string, text: string): string | undefined {
  const what = `HTTP ${status}${type ? `, ${type}` : ""}`;
  if (status < 200 || status > 299) return what;
  const why = /html/i.test(type) ? (notAMap(text) ?? "an HTML page's type") : notAMap(text);
  return why ? `${what}: ${why}, not a source map` : undefined;
}

/** Through the page first (the test's own routing, the app's origin), else from the test process. */
async function fetchMap(page: Page, url: string): Promise<{ text: string } | { why: string }> {
  const why: string[] = [];
  try {
    const r = await page.evaluate(
      async ([u, ms]) => {
        try {
          const res = await fetch(u as string, { signal: AbortSignal.timeout(ms as number) });
          return {
            status: res.status,
            type: res.headers.get("content-type") ?? "",
            text: res.ok ? await res.text() : "",
          };
        } catch (e) {
          return { error: String(e) };
        }
      },
      [url, mapFetchSeconds * 1000],
    );
    if ("error" in r) why.push(`in the page: ${r.error}`);
    else {
      const no = notAMapAnswer(r.status, r.type, r.text);
      if (!no) return { text: r.text };
      why.push(`in the page: ${no}`);
    }
  } catch (e) {
    why.push(`in the page: ${(e as Error).message.split("\n")[0]}`);
  }
  try {
    const r = await fetch(url, { signal: AbortSignal.timeout(mapFetchSeconds * 1000) });
    const text = r.ok ? await r.text() : "";
    const no = notAMapAnswer(r.status, r.headers.get("content-type") ?? "", text);
    if (!no) return { text };
    why.push(`from the test process: ${no}`);
  } catch (e) {
    why.push(`from the test process: ${(e as Error).message}`);
  }
  return { why: `${url}: ${why.join("; ")}` };
}

/** Batches and workers may write the same file. */
function writeOnce(path: string, data: string): void {
  if (existsSync(path)) return;
  const tmp = `${path}.${process.pid}.${Math.random().toString(36).slice(2)}.tmp`;
  writeFileSync(tmp, data);
  renameSync(tmp, path);
}

function inlineMap(source: string): NodeScript["map"] | undefined {
  const m =
    /\/\/[#@] sourceMappingURL=data:application\/json[^,]*;base64,([A-Za-z0-9+/=]+)\s*$/.exec(
      source.slice(-1_000_000),
    );
  if (!m?.[1]) return undefined;
  const text = Buffer.from(m[1], "base64").toString("utf8");
  if (notAMap(text)) return undefined;
  return JSON.parse(text);
}

class Recorder {
  readonly opt: CoverageOptions;
  readonly name: string;
  browserName = "chromium";
  supported = true;
  private scripts = new Map<string, Acc>();
  private requests = new Set<string>();
  private documents = new Set<string>();
  private readonly pages = new Set<Page>();
  private readonly contexts = new WeakSet<BrowserContext>();
  private session?: Session;
  private readonly sources = new Map<string, string>();
  current?: string;
  setupDone = false;
  /**
   * The marks directory, in a bundle whose spec files pipelinegen marks:
   * the servers the tests talk to are recorded per spec file between the
   * marks.
   */
  marks?: string;
  /** The test the worker ran last, whose status at the worker's end says whether the run was cut. */
  last?: TestInfo;

  constructor(opt: CoverageOptions) {
    this.opt = opt;
    this.name = opt.name ?? "react";
  }

  dir(slice: string): string {
    return join(this.opt.out as string, slice, this.name);
  }

  async startNode(): Promise<void> {
    if (!this.opt.node) return;
    const s = new Session();
    s.connect();
    await s.post("Profiler.enable");
    await s.post("Profiler.startPreciseCoverage", { callCount: true, detailed: true });
    await s.post("Debugger.enable");
    this.session = s;
  }

  track(context: BrowserContext): void {
    if (this.contexts.has(context)) return;
    this.contexts.add(context);
    context.on("request", (r) => {
      try {
        const u = new URL(r.url());
        if (u.protocol !== "http:" && u.protocol !== "https:") return;
        this.requests.add(`${u.origin}${u.pathname}`);
        // A page's own document, not a frame inside it.
        if (r.isNavigationRequest() && r.frame().parentFrame() === null)
          this.documents.add(`${u.origin}${u.pathname}`);
      } catch {
        // not a URL coverage can name
      }
    });
    for (const p of context.pages()) void this.cover(p);
    context.on("page", (p) => void this.cover(p));
    const close = context.close.bind(context);
    context.close = async (options) => {
      for (const p of context.pages()) await this.uncover(p);
      return close(options);
    };
  }

  async cover(page: Page): Promise<void> {
    if (this.pages.has(page)) return;
    if (this.browserName !== "chromium") {
      this.supported = false;
      return;
    }
    this.pages.add(page);
    try {
      await page.coverage.startJSCoverage({
        resetOnNavigation: false,
        reportAnonymousScripts: false,
      });
    } catch {
      this.pages.delete(page);
      return;
    }
    if (this.closing.has(page)) return;
    this.closing.add(page);
    const close = page.close.bind(page);
    page.close = async (options) => {
      await this.uncover(page);
      return close(options);
    };
  }

  /** Pages whose close reads their coverage first. */
  private readonly closing = new WeakSet<Page>();

  async uncover(page: Page): Promise<void> {
    if (!this.pages.delete(page)) return;
    let entries: Awaited<ReturnType<Page["coverage"]["stopJSCoverage"]>>;
    try {
      entries = await page.coverage.stopJSCoverage();
    } catch {
      return; // the page is gone; what it ran is lost with it
    }
    for (const e of entries) {
      if (!/^https?:/.test(e.url)) continue;
      const fns = e.functions as FunctionCoverage[];
      const acc = this.scripts.get(e.url) ?? {
        sha: sha16(e.source ?? ""),
        size: Buffer.byteLength(e.source ?? ""),
        ran: [],
        fns: new Set(),
      };
      if (!this.scripts.has(e.url) && e.source !== undefined)
        await this.save(page, e.url, acc.sha, e.source);
      acc.ran = union(acc.ran, ranIntervals(fns));
      for (const f of e.functions) {
        const r = f.ranges[0];
        if (r && r.count > 0) acc.fns.add(r.startOffset);
      }
      this.scripts.set(e.url, acc);
    }
  }

  private readonly saved = new Set<string>();

  /**
   * Keeps a served script and its source map with the recording, once per
   * sha, so observe needs no build where it runs. A map that can't be
   * fetched is left out, and observe falls back to the build for that
   * script rather than read it as one without a map.
   */
  private async save(page: Page, url: string, sha: string, source: string): Promise<void> {
    if (!this.opt.out || this.saved.has(sha)) return;
    this.saved.add(sha);
    const dir = join(this.dir("_setup"), scriptsDir);
    mkdirSync(dir, { recursive: true });
    const ref = mapRef(source);
    if (!ref) writeOnce(join(dir, `${sha}.nomap`), "");
    else if (ref.startsWith("data:")) {
      const map = inlineMap(source);
      if (map) writeOnce(join(dir, `${sha}.map`), JSON.stringify(map));
      else writeOnce(join(dir, `${sha}${mapFailSuffix}`), "the script's inline map can't be read");
    } else {
      const map = await fetchMap(page, new URL(ref, url).href);
      // Without its map, observe reads the build for this script.
      if ("text" in map) writeOnce(join(dir, `${sha}.map`), map.text);
      else writeOnce(join(dir, `${sha}${mapFailSuffix}`), map.why);
    }
    writeOnce(join(dir, `${sha}.js`), source);
  }

  async uncoverAll(): Promise<void> {
    for (const p of [...this.pages]) await this.uncover(p);
  }

  /**
   * Reads what the pages still open ran, and goes on covering them: a page
   * a spec file's beforeAll hook opened serves its next tests too.
   */
  async readOpen(): Promise<void> {
    for (const p of [...this.pages]) {
      await this.uncover(p);
      if (!p.isClosed()) await this.cover(p);
    }
  }

  private async nodeRecord(): Promise<NodeRecord> {
    const scripts: NodeScript[] = [];
    if (!this.session) return { version: 1, scripts };
    const res = (await this.session.post("Profiler.takePreciseCoverage")) as {
      result: { scriptId: string; url: string; functions: FunctionCoverage[] }[];
    };
    const root = resolve(this.opt.root);
    for (const sc of res.result) {
      if (!sc.url.startsWith("file:")) continue;
      const abs = fileURLToPath(sc.url);
      const rel = relative(root, abs);
      if (rel.startsWith("..") || isAbsolute(rel) || rel.split(sep).includes("node_modules"))
        continue;
      const ran = ranIntervals(sc.functions);
      if (ran.length === 0) continue;
      let source = this.sources.get(sc.scriptId);
      if (source === undefined) {
        const r = (await this.session.post("Debugger.getScriptSource", {
          scriptId: sc.scriptId,
        })) as {
          scriptSource: string;
        };
        source = r.scriptSource;
        this.sources.set(sc.scriptId, source);
      }
      const map = inlineMap(source);
      if (map) {
        // The map's sources are relative to the script; name them from the root.
        const base = dirname(abs);
        const sr = (map as { sourceRoot?: string }).sourceRoot ?? "";
        map.sources = map.sources.map((s) =>
          relative(root, resolve(base, sr, s))
            .split(sep)
            .join(posix.sep),
        );
        delete (map as { sourceRoot?: string }).sourceRoot;
      }
      const fns: number[] = [];
      for (const f of sc.functions) {
        const r = f.ranges[0];
        if (r && r.count > 0) fns.push(r.startOffset);
      }
      scripts.push({
        file: rel.split(sep).join(posix.sep),
        source,
        ...(map ? { map } : {}),
        ran,
        fns,
      });
    }
    return { version: 1, scripts };
  }

  /** Writes what ran since the last write as a new part in `dir`, and starts afresh. */
  async write(dir: string): Promise<void> {
    await this.uncoverAll();
    const browser: BrowserRecord = {
      version: 1,
      browser: this.browserName,
      supported: this.supported,
      scripts: [...this.scripts].map(
        ([url, a]): BrowserScript => ({
          url,
          sha: a.sha,
          size: a.size,
          ran: a.ran,
          fns: [...a.fns].sort((x, y) => x - y),
        }),
      ),
      requests: [...this.requests].sort(),
      documents: [...this.documents].sort(),
    };
    const node = await this.nodeRecord();
    this.scripts = new Map();
    this.requests = new Set();
    this.documents = new Set();
    // A part of its own: the slice may already hold one from another project.
    const part = newPart(dir);
    writeFileSync(join(part, browserFile), JSON.stringify(browser));
    writeFileSync(join(part, nodeFile), JSON.stringify(node));
    writeFileSync(join(part, doneFile), "");
  }

  async endSlice(): Promise<void> {
    const slice = this.current;
    if (slice === undefined) return;
    this.current = undefined;
    const dir = this.dir(slice);
    // In a marked bundle, a test the run's stop cut leaves its spec file unfinished: no done marker and
    // no stop mark, so neither recording can be taken for the whole spec file.
    if (this.marks && this.last?.status === "interrupted") {
      newPart(dir);
      return;
    }
    await this.write(dir);
    await this.opt.onSliceEnd?.(slice, dir);
    // After the part is written: pipelinegen reads it when it takes the stop.
    if (this.marks) await markStop(this.marks, slice);
  }

  async startSlice(slice: string): Promise<void> {
    if (!this.setupDone) {
      this.setupDone = true;
      const dir = this.dir("_setup");
      await this.write(dir);
      await this.opt.onSetupEnd?.(dir);
    }
    // A mark not taken throws, so the spec file's tests don't run unmarked.
    if (this.marks) await markStart(this.marks, slice);
    this.current = slice;
    await this.opt.onSliceStart?.(slice, this.dir(slice));
  }

  /** Ends the spec file open, if another one's code is about to run. */
  async enter(file: string): Promise<void> {
    const slice = relative(resolve(this.opt.root), file).split(sep).join(posix.sep);
    if (this.current === slice) return;
    await this.endSlice();
    await this.startSlice(slice);
  }
}

type CoverageFixtures = { _reactCoverageFile: undefined; _reactCoverageTest: undefined };
type CoverageWorkerFixtures = { _reactCoverage: Recorder | undefined };

/**
 * Extends a Playwright `test` with the recording. Run the batch with one worker, so each spec file's
 * directory holds only what that file ran.
 */
export function withCoverage<T extends object, W extends object>(
  base: TestType<T, W>,
  opt: CoverageOptions,
): TestType<T & CoverageFixtures, W & CoverageWorkerFixtures> {
  const b = base as unknown as TestType<
    { context: BrowserContext; page: Page },
    { browser: Browser; browserName: string }
  >;
  const extended = b.extend<CoverageFixtures, CoverageWorkerFixtures>({
    _reactCoverage: [
      async ({ browserName }, use, worker) => {
        if (!opt.out) {
          await use(undefined);
          return;
        }
        const r = new Recorder(opt);
        r.browserName = browserName;
        r.marks = marksDir();
        // One stack serves the bundle: two spec files at once would share its coverage between the marks.
        if (r.marks && worker.config.workers > 1)
          throw new Error(
            `react-correlator: this bundle's spec files are marked one at a time, so it runs with one worker, but this run has ${worker.config.workers}: remove the --workers the suite's flags add`,
          );
        await r.startNode();
        await use(r);
        await r.endSlice();
      },
      { scope: "worker", auto: true },
    ],
    // contexts a spec opens on the browser itself are recorded too
    browser: [
      async ({ browser, _reactCoverage }, use) => {
        if (_reactCoverage) {
          const newContext = browser.newContext.bind(browser);
          browser.newContext = async (options) => {
            const c = await newContext(options);
            _reactCoverage.track(c);
            return c;
          };
          const newPage = browser.newPage.bind(browser);
          browser.newPage = async (options) => {
            const p = await newPage(options);
            _reactCoverage.track(p.context());
            return p;
          };
        }
        await use(browser);
      },
      { scope: "worker" },
    ],
    // the test's recording is set up first, so a new spec file's pages never
    // land in the previous file's directory
    context: async ({ context, _reactCoverage, _reactCoverageTest: _ }, use) => {
      _reactCoverage?.track(context);
      await use(context);
    },
    // a new spec file starts before its first hook runs: Playwright sets up
    // an auto fixture marked "all-hooks-included" around beforeAll and
    // afterAll hooks as well as tests (its own tracing fixture is one; its
    // types leave the value out, and 1.40.0 and 1.63.0 both take it)
    _reactCoverageFile: [
      async ({ _reactCoverage }, use, info) => {
        // The hooks Playwright runs while it stops have no file.
        if (_reactCoverage && info.file) {
          await _reactCoverage.enter(info.file);
          // A hook the run's stop cuts leaves its spec file unfinished too.
          _reactCoverage.last = info;
        }
        await use(undefined);
      },
      { auto: "all-hooks-included" as unknown as true },
    ],
    _reactCoverageTest: [
      async ({ _reactCoverage, _reactCoverageFile: _ }, use, info) => {
        const r = _reactCoverage;
        if (r) {
          await r.enter(info.file);
          r.last = info;
        }
        await use(undefined);
        // Read pages still open before their context closes, and go on covering them.
        await r?.readOpen();
      },
      { auto: true },
    ],
  });
  return extended as unknown as TestType<T & CoverageFixtures, W & CoverageWorkerFixtures>;
}
