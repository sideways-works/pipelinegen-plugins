// The Playwright fixture: it records, per spec file, every probe the app's
// i18next instances made in a test's pages, into <out>/<spec file>/.
//
// The app records only in a build that calls the hook, behind a build-time
// flag production builds compile out:
//
//   if (import.meta.env.VITE_PIPELINEGEN_LOCALES)
//     globalThis.__pipelinegenLocales?.(i18n, "apps/web/src/locales");
//
// A document matching `expect` that never called the hook by its load event
// was built without the flag, and the slice can't tell.
import { mkdirSync, writeFileSync, existsSync } from "node:fs";
import { join, posix, relative, resolve, sep } from "node:path";
import { localeKeysCore } from "./core.mjs";

const BINDING = "__pipelinegenLocaleKeys";

/** The script every page runs first: the core, and the hook the app calls. */
function initScript(expect) {
  return `(() => {
  if (globalThis.__pipelinegenLocales) return;
  const send = (r) => { try { globalThis.${BINDING}(r); } catch (e) {} };
  const core = (${localeKeysCore.toString()})(send);
  let called = false;
  globalThis.__pipelinegenLocales = (i18n, catalog) => { called = true; core.install(i18n, catalog); };
  const expect = ${JSON.stringify(expect)}.map((s) => new RegExp(s));
  addEventListener("load", () => {
    if (!called && expect.some((re) => re.test(location.href)))
      send({ cant: "the page " + location.origin + location.pathname + " loaded the app without the recorder (a build without VITE_PIPELINEGEN_LOCALES)" });
  });
})();`;
}

class Recorder {
  constructor(opt) {
    this.opt = opt;
    this.records = [];
    this.seen = new Set();
    this.contexts = new WeakSet();
    this.parts = 0;
    this.script = initScript((opt.expect ?? []).map((e) => (e instanceof RegExp ? e.source : String(e))));
  }

  async track(context) {
    if (this.contexts.has(context)) return;
    this.contexts.add(context);
    await context.exposeBinding(BINDING, (_source, r) => {
      const s = JSON.stringify(r);
      if (this.seen.has(s)) return;
      this.seen.add(s);
      this.records.push(r);
    });
    await context.addInitScript({ content: this.script });
  }

  /** Writes what the test's pages recorded as one file of the spec file's directory. */
  async write(context, file) {
    // a round trip through each page delivers the records it sent before
    for (const p of context?.pages() ?? []) await p.evaluate(() => 0).catch(() => {});
    const slice = relative(resolve(this.opt.root), file).split(sep).join(posix.sep);
    const dir = join(this.opt.out, slice);
    mkdirSync(dir, { recursive: true });
    const head = { recorder: "locale-keys", version: 1, runtime: "browser", pid: process.pid };
    const lines = [head, ...this.records, { end: true }].map((r) => JSON.stringify(r));
    for (;;) {
      this.parts++;
      const name = join(dir, `locale-keys.browser-${process.pid}-${this.parts}.jsonl`);
      if (existsSync(name)) continue;
      writeFileSync(name, `${lines.join("\n")}\n`, { flag: "wx" });
      break;
    }
    this.records = [];
    this.seen = new Set();
  }
}

/**
 * Extends a Playwright `test` with the recording. Options: `out`, where
 * to write (unset records nothing); `root`, the repository's root, spec
 * files named from it; `expect`, addresses (RegExp or its source) of the
 * app's documents, each of which must call the hook.
 */
export function withLocaleKeys(base, opt) {
  return base.extend({
    _localeKeys: [
      async ({}, use) => {
        await use(opt.out ? new Recorder(opt) : undefined);
      },
      { scope: "worker" },
    ],
    browser: [
      async ({ browser, _localeKeys }, use) => {
        if (_localeKeys) {
          const newContext = browser.newContext.bind(browser);
          browser.newContext = async (options) => {
            const c = await newContext(options);
            await _localeKeys.track(c);
            return c;
          };
          const newPage = browser.newPage.bind(browser);
          browser.newPage = async (options) => {
            const p = await newPage(options);
            await _localeKeys.track(p.context());
            return p;
          };
        }
        await use(browser);
      },
      { scope: "worker" },
    ],
    context: async ({ context, _localeKeys }, use, info) => {
      await _localeKeys?.track(context);
      await use(context);
      await _localeKeys?.write(context, info.file);
    },
  });
}
