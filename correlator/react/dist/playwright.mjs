// src/playwright.ts
import { createHash } from "node:crypto";
import { existsSync as existsSync3, mkdirSync as mkdirSync2, renameSync as renameSync3, writeFileSync as writeFileSync3 } from "node:fs";
import { Session } from "node:inspector/promises";
import { dirname as dirname2, isAbsolute, join as join3, posix, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

// src/intervals.ts
function ranIntervals(functions) {
  const ranges = [];
  for (const f of functions)
    for (const r of f.ranges) if (r.endOffset > r.startOffset) ranges.push(r);
  ranges.sort((a, b) => a.startOffset - b.startOffset || b.endOffset - a.endOffset);
  const points = /* @__PURE__ */ new Set();
  for (const r of ranges) {
    points.add(r.startOffset);
    points.add(r.endOffset);
  }
  const ps = [...points].sort((a, b) => a - b);
  const out = [];
  const stack = [];
  let next = 0;
  for (let i = 0; i + 1 < ps.length; i++) {
    const a = ps[i];
    const b = ps[i + 1];
    while (stack.length && stack[stack.length - 1].endOffset <= a) stack.pop();
    while (next < ranges.length && ranges[next].startOffset <= a) {
      const r = ranges[next++];
      while (stack.length && stack[stack.length - 1].endOffset <= r.startOffset)
        stack.pop();
      if (r.endOffset > a) stack.push(r);
    }
    const top = stack[stack.length - 1];
    if (top && top.count > 0) push(out, a, b);
  }
  return out;
}
function push(out, a, b) {
  const last = out[out.length - 1];
  if (last && last[1] >= a) last[1] = Math.max(last[1], b);
  else out.push([a, b]);
}
function union(x, y) {
  const all = [...x, ...y].sort((a, b) => a[0] - b[0]);
  const out = [];
  for (const [a, b] of all) push(out, a, b);
  return out;
}

// src/marks.ts
import { existsSync, readFileSync, renameSync, statSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { setTimeout as sleep } from "node:timers/promises";
var marksEnv = "PIPELINEGEN_MARKS";
var markSeconds = 120;
var stateKey = /* @__PURE__ */ Symbol.for("pipelinegen.marks");
function state() {
  const g = globalThis;
  g[stateKey] ??= { n: 0 };
  return g[stateKey];
}
function marksDir() {
  const dir = process.env[marksEnv];
  if (!dir) return void 0;
  try {
    return statSync(dir).isDirectory() ? dir : void 0;
  } catch {
    return void 0;
  }
}
async function exchange(dir, mark, slice, seconds) {
  const s = state();
  s.n++;
  const id = `${Date.now()}-${process.pid}-${String(s.n).padStart(6, "0")}`;
  const tmp = join(dir, `.${id}.tmp`);
  writeFileSync(tmp, JSON.stringify({ mark, slice }));
  renameSync(tmp, join(dir, `${id}.json`));
  const deadline = Date.now() + seconds * 1e3;
  for (; ; ) {
    if (existsSync(join(dir, `${id}.done`))) return;
    const failed = join(dir, `${id}.failed`);
    if (existsSync(failed))
      throw new Error(`pipelinegen mark ${mark} ${slice}: ${readFileSync(failed, "utf8")}`);
    if (Date.now() > deadline)
      throw new Error(
        `pipelinegen mark ${mark} ${slice}: the batch did not answer within ${seconds}s in ${dir}`
      );
    await sleep(20);
  }
}
async function markStart(dir, slice, seconds = markSeconds) {
  const s = state();
  if (s.open === slice) return;
  if (s.open !== void 0) await markStop(dir, s.open, seconds);
  await exchange(dir, "start", slice, seconds);
  s.open = slice;
}
async function markStop(dir, slice, seconds = markSeconds) {
  const s = state();
  if (s.open !== slice) return;
  s.open = void 0;
  await exchange(dir, "stop", slice, seconds);
}

// src/record.ts
import {
  existsSync as existsSync2,
  mkdirSync,
  readdirSync,
  readFileSync as readFileSync2,
  renameSync as renameSync2,
  statSync as statSync2,
  writeFileSync as writeFileSync2
} from "node:fs";
import { basename, dirname, join as join2 } from "node:path";
var doneFile = "done";
var scriptsDir = "scripts";
var mapFailSuffix = ".mapfail";
var browserFile = "browser.json";
var nodeFile = "node.json";
function newPart(dir) {
  mkdirSync(dir, { recursive: true });
  for (let n = 1; ; n++) {
    const part = join2(dir, String(n));
    try {
      mkdirSync(part);
      return part;
    } catch (e) {
      if (e.code !== "EEXIST") throw e;
    }
  }
}
function mapRef(source) {
  const m = /\/\/[#@] sourceMappingURL=([^\s'"]+)\s*$/.exec(source.slice(-2e3));
  return m?.[1];
}
function kindOf(text) {
  const head = text.trimStart();
  if (head === "") return "it is empty";
  if (/^<(!doctype\s+html|html)\b/i.test(head)) return "it is an HTML document";
  if (head.startsWith("<")) return "it is markup";
  const shown = JSON.stringify(head.slice(0, 16));
  return head.startsWith("{") || head.startsWith("[") ? `it is not JSON a source map can be read from (it starts ${shown})` : `it is not JSON (it starts ${shown})`;
}
function notAMap(text) {
  let m;
  try {
    m = JSON.parse(text);
  } catch {
    return kindOf(text);
  }
  if (typeof m !== "object" || m === null) return kindOf(text);
  const o = m;
  if (Array.isArray(o.sections)) return void 0;
  if (typeof o.mappings === "string" && Array.isArray(o.sources)) return void 0;
  return "it is JSON, but no source map: it has no mappings and sources, nor sections";
}

// src/playwright.ts
function sha16(text) {
  return createHash("sha256").update(text).digest("hex").slice(0, 16);
}
var mapFetchSeconds = 60;
function notAMapAnswer(status, type, text) {
  const what = `HTTP ${status}${type ? `, ${type}` : ""}`;
  if (status < 200 || status > 299) return what;
  const why = /html/i.test(type) ? notAMap(text) ?? "an HTML page's type" : notAMap(text);
  return why ? `${what}: ${why}, not a source map` : void 0;
}
async function fetchMap(page, url) {
  const why = [];
  try {
    const r = await page.evaluate(
      async ([u, ms]) => {
        try {
          const res = await fetch(u, { signal: AbortSignal.timeout(ms) });
          return {
            status: res.status,
            type: res.headers.get("content-type") ?? "",
            text: res.ok ? await res.text() : ""
          };
        } catch (e) {
          return { error: String(e) };
        }
      },
      [url, mapFetchSeconds * 1e3]
    );
    if ("error" in r) why.push(`in the page: ${r.error}`);
    else {
      const no = notAMapAnswer(r.status, r.type, r.text);
      if (!no) return { text: r.text };
      why.push(`in the page: ${no}`);
    }
  } catch (e) {
    why.push(`in the page: ${e.message.split("\n")[0]}`);
  }
  try {
    const r = await fetch(url, { signal: AbortSignal.timeout(mapFetchSeconds * 1e3) });
    const text = r.ok ? await r.text() : "";
    const no = notAMapAnswer(r.status, r.headers.get("content-type") ?? "", text);
    if (!no) return { text };
    why.push(`from the test process: ${no}`);
  } catch (e) {
    why.push(`from the test process: ${e.message}`);
  }
  return { why: `${url}: ${why.join("; ")}` };
}
function writeOnce(path, data) {
  if (existsSync3(path)) return;
  const tmp = `${path}.${process.pid}.${Math.random().toString(36).slice(2)}.tmp`;
  writeFileSync3(tmp, data);
  renameSync3(tmp, path);
}
function inlineMap(source) {
  const m = /\/\/[#@] sourceMappingURL=data:application\/json[^,]*;base64,([A-Za-z0-9+/=]+)\s*$/.exec(
    source.slice(-1e6)
  );
  if (!m?.[1]) return void 0;
  const text = Buffer.from(m[1], "base64").toString("utf8");
  if (notAMap(text)) return void 0;
  return JSON.parse(text);
}
var Recorder = class {
  opt;
  name;
  browserName = "chromium";
  supported = true;
  scripts = /* @__PURE__ */ new Map();
  requests = /* @__PURE__ */ new Set();
  documents = /* @__PURE__ */ new Set();
  pages = /* @__PURE__ */ new Set();
  contexts = /* @__PURE__ */ new WeakSet();
  session;
  sources = /* @__PURE__ */ new Map();
  current;
  setupDone = false;
  /**
   * The marks directory, in a bundle whose spec files pipelinegen marks:
   * the servers the tests talk to are recorded per spec file between the
   * marks.
   */
  marks;
  /** The test the worker ran last, whose status at the worker's end says whether the run was cut. */
  last;
  constructor(opt) {
    this.opt = opt;
    this.name = opt.name ?? "react";
  }
  dir(slice) {
    return join3(this.opt.out, slice, this.name);
  }
  async startNode() {
    if (!this.opt.node) return;
    const s = new Session();
    s.connect();
    await s.post("Profiler.enable");
    await s.post("Profiler.startPreciseCoverage", { callCount: true, detailed: true });
    await s.post("Debugger.enable");
    this.session = s;
  }
  track(context) {
    if (this.contexts.has(context)) return;
    this.contexts.add(context);
    context.on("request", (r) => {
      try {
        const u = new URL(r.url());
        if (u.protocol !== "http:" && u.protocol !== "https:") return;
        this.requests.add(`${u.origin}${u.pathname}`);
        if (r.isNavigationRequest() && r.frame().parentFrame() === null)
          this.documents.add(`${u.origin}${u.pathname}`);
      } catch {
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
  async cover(page) {
    if (this.pages.has(page)) return;
    if (this.browserName !== "chromium") {
      this.supported = false;
      return;
    }
    this.pages.add(page);
    try {
      await page.coverage.startJSCoverage({
        resetOnNavigation: false,
        reportAnonymousScripts: false
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
  closing = /* @__PURE__ */ new WeakSet();
  async uncover(page) {
    if (!this.pages.delete(page)) return;
    let entries;
    try {
      entries = await page.coverage.stopJSCoverage();
    } catch {
      return;
    }
    for (const e of entries) {
      if (!/^https?:/.test(e.url)) continue;
      const fns = e.functions;
      const acc = this.scripts.get(e.url) ?? {
        sha: sha16(e.source ?? ""),
        size: Buffer.byteLength(e.source ?? ""),
        ran: [],
        fns: /* @__PURE__ */ new Set()
      };
      if (!this.scripts.has(e.url) && e.source !== void 0)
        await this.save(page, e.url, acc.sha, e.source);
      acc.ran = union(acc.ran, ranIntervals(fns));
      for (const f of e.functions) {
        const r = f.ranges[0];
        if (r && r.count > 0) acc.fns.add(r.startOffset);
      }
      this.scripts.set(e.url, acc);
    }
  }
  saved = /* @__PURE__ */ new Set();
  /**
   * Keeps a served script and its source map with the recording, once per
   * sha, so observe needs no build where it runs. A map that can't be
   * fetched is left out, and observe falls back to the build for that
   * script rather than read it as one without a map.
   */
  async save(page, url, sha, source) {
    if (!this.opt.out || this.saved.has(sha)) return;
    this.saved.add(sha);
    const dir = join3(this.dir("_setup"), scriptsDir);
    mkdirSync2(dir, { recursive: true });
    const ref = mapRef(source);
    if (!ref) writeOnce(join3(dir, `${sha}.nomap`), "");
    else if (ref.startsWith("data:")) {
      const map = inlineMap(source);
      if (map) writeOnce(join3(dir, `${sha}.map`), JSON.stringify(map));
      else writeOnce(join3(dir, `${sha}${mapFailSuffix}`), "the script's inline map can't be read");
    } else {
      const map = await fetchMap(page, new URL(ref, url).href);
      if ("text" in map) writeOnce(join3(dir, `${sha}.map`), map.text);
      else writeOnce(join3(dir, `${sha}${mapFailSuffix}`), map.why);
    }
    writeOnce(join3(dir, `${sha}.js`), source);
  }
  async uncoverAll() {
    for (const p of [...this.pages]) await this.uncover(p);
  }
  /**
   * Reads what the pages still open ran, and goes on covering them: a page
   * a spec file's beforeAll hook opened serves its next tests too.
   */
  async readOpen() {
    for (const p of [...this.pages]) {
      await this.uncover(p);
      if (!p.isClosed()) await this.cover(p);
    }
  }
  async nodeRecord() {
    const scripts = [];
    if (!this.session) return { version: 1, scripts };
    const res = await this.session.post("Profiler.takePreciseCoverage");
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
      if (source === void 0) {
        const r = await this.session.post("Debugger.getScriptSource", {
          scriptId: sc.scriptId
        });
        source = r.scriptSource;
        this.sources.set(sc.scriptId, source);
      }
      const map = inlineMap(source);
      if (map) {
        const base = dirname2(abs);
        const sr = map.sourceRoot ?? "";
        map.sources = map.sources.map(
          (s) => relative(root, resolve(base, sr, s)).split(sep).join(posix.sep)
        );
        delete map.sourceRoot;
      }
      const fns = [];
      for (const f of sc.functions) {
        const r = f.ranges[0];
        if (r && r.count > 0) fns.push(r.startOffset);
      }
      scripts.push({
        file: rel.split(sep).join(posix.sep),
        source,
        ...map ? { map } : {},
        ran,
        fns
      });
    }
    return { version: 1, scripts };
  }
  /** Writes what ran since the last write as a new part in `dir`, and starts afresh. */
  async write(dir) {
    await this.uncoverAll();
    const browser = {
      version: 1,
      browser: this.browserName,
      supported: this.supported,
      scripts: [...this.scripts].map(
        ([url, a]) => ({
          url,
          sha: a.sha,
          size: a.size,
          ran: a.ran,
          fns: [...a.fns].sort((x, y) => x - y)
        })
      ),
      requests: [...this.requests].sort(),
      documents: [...this.documents].sort()
    };
    const node = await this.nodeRecord();
    this.scripts = /* @__PURE__ */ new Map();
    this.requests = /* @__PURE__ */ new Set();
    this.documents = /* @__PURE__ */ new Set();
    const part = newPart(dir);
    writeFileSync3(join3(part, browserFile), JSON.stringify(browser));
    writeFileSync3(join3(part, nodeFile), JSON.stringify(node));
    writeFileSync3(join3(part, doneFile), "");
  }
  async endSlice() {
    const slice = this.current;
    if (slice === void 0) return;
    this.current = void 0;
    const dir = this.dir(slice);
    if (this.marks && this.last?.status === "interrupted") {
      newPart(dir);
      return;
    }
    await this.write(dir);
    await this.opt.onSliceEnd?.(slice, dir);
    if (this.marks) await markStop(this.marks, slice);
  }
  async startSlice(slice) {
    if (!this.setupDone) {
      this.setupDone = true;
      const dir = this.dir("_setup");
      await this.write(dir);
      await this.opt.onSetupEnd?.(dir);
    }
    if (this.marks) await markStart(this.marks, slice);
    this.current = slice;
    await this.opt.onSliceStart?.(slice, this.dir(slice));
  }
  /** Ends the spec file open, if another one's code is about to run. */
  async enter(file) {
    const slice = relative(resolve(this.opt.root), file).split(sep).join(posix.sep);
    if (this.current === slice) return;
    await this.endSlice();
    await this.startSlice(slice);
  }
};
function withCoverage(base, opt) {
  const b = base;
  const extended = b.extend({
    _reactCoverage: [
      async ({ browserName }, use, worker) => {
        if (!opt.out) {
          await use(void 0);
          return;
        }
        const r = new Recorder(opt);
        r.browserName = browserName;
        r.marks = marksDir();
        if (r.marks && worker.config.workers > 1)
          throw new Error(
            `react-correlator: this bundle's spec files are marked one at a time, so it runs with one worker, but this run has ${worker.config.workers}: remove the --workers the suite's flags add`
          );
        await r.startNode();
        await use(r);
        await r.endSlice();
      },
      { scope: "worker", auto: true }
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
      { scope: "worker" }
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
        if (_reactCoverage && info.file) {
          await _reactCoverage.enter(info.file);
          _reactCoverage.last = info;
        }
        await use(void 0);
      },
      { auto: "all-hooks-included" }
    ],
    _reactCoverageTest: [
      async ({ _reactCoverage, _reactCoverageFile: _ }, use, info) => {
        const r = _reactCoverage;
        if (r) {
          await r.enter(info.file);
          r.last = info;
        }
        await use(void 0);
        await r?.readOpen();
      },
      { auto: true }
    ]
  });
  return extended;
}
export {
  mapFetchSeconds,
  mapRef,
  withCoverage
};
