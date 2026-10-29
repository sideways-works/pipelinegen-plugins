// The vitest setup file: with PIPELINEGEN_LOCALE_KEYS_OUT set, it records
// every probe of every i18next instance a test file makes into
// <out>/<test file>/. Name it in the vitest config's setupFiles.
//
// A test's own instance names no catalog, so a bundle is matched to the
// catalog file with its content, or with exactly its keys when a build step
// changed the values. A bundle that matches no file but shares keys with one
// can't be named, and the test file can't tell.
import { readdirSync, readFileSync, mkdirSync, writeFileSync, existsSync, statSync } from "node:fs";
import { dirname, join, relative, sep, posix, resolve } from "node:path";
import i18next from "i18next";
import { afterAll, expect } from "vitest";
import { localeKeysCore } from "./core.mjs";

const out = process.env.PIPELINEGEN_LOCALE_KEYS_OUT;
const STATE = Symbol.for("pipelinegen.localeKeys.vitest");
const WRAPPED = Symbol.for("pipelinegen.localeKeys.wrapped");

if (out) {
  const state = (globalThis[STATE] ??= setUp());
  state.records.length = 0;
  state.core.reset();
  // a test file may get a fresh copy of the module: wrap each copy's class
  state.patch(i18next);
  globalThis.__pipelinegenLocales = (i18n, catalog) => state.core.install(i18n, catalog);

  afterAll(() => {
    const file = expect.getState().testPath;
    if (!file) return;
    const slice = relative(state.root, file).split(sep).join(posix.sep);
    const dir = join(out, slice);
    mkdirSync(dir, { recursive: true });
    const head = { recorder: "locale-keys", version: 1, runtime: "vitest", pid: process.pid };
    const lines = [head, ...state.records, { end: true }].map((r) => JSON.stringify(r));
    for (;;) {
      state.parts++;
      const name = join(dir, `locale-keys.vitest-${process.pid}-${state.parts}.jsonl`);
      if (existsSync(name)) continue;
      writeFileSync(name, `${lines.join("\n")}\n`, { flag: "wx" });
      break;
    }
    state.records.length = 0;
    state.core.reset();
  });
}

/** The process's recorder; patch(i18next) wraps a copy's store class once, which every instance of that copy shares. */
function setUp() {
  const records = [];
  const root = findRoot();
  const core = localeKeysCore((r) => records.push(r));
  const index = lazyIndex(root);

  // which catalog files a store's bundle of lng and ns came from
  const sources = new WeakMap();
  const byNsOf = (store) => {
    let byNs = sources.get(store);
    if (!byNs) sources.set(store, (byNs = new Map()));
    return byNs;
  };
  const match = (bundle, lng, ns) => {
    if (!bundle || typeof bundle !== "object") return [];
    let files = index().byContent.get(canonical(bundle)) ?? [];
    if (files.length === 0) {
      // the same keys, values a build step changed
      const same = index().byKeys.get(keySignature(bundle)) ?? [];
      const named = same.filter((f) => f.endsWith(`/${lng}/${ns}.json`) || f.endsWith(`/${lng}.json`));
      files = same.length === 1 ? same : named.length === 1 ? named : [];
    }
    if (files.length === 0 && sharesKeys(bundle, index().keys))
      core.put({
        cant: `an i18next bundle (${lng}/${ns}) matches no catalog file but shares keys with one, so its lookups can't be named`,
      });
    return files;
  };
  const filesOf = (store, lng, ns) => {
    const byNs = byNsOf(store);
    const k = `${lng}\u0000${ns}`;
    if (!byNs.has(k)) byNs.set(k, match(store.data?.[lng]?.[ns], lng, ns));
    return byNs.get(k);
  };
  const targets = (store, lng, ns) => filesOf(store, lng, ns).map((f) => ({ f }));

  const patch = (lib) => {
    const probe = lib.createInstance();
    probe.init({ initAsync: false, initImmediate: false, resources: {} });
    const proto = Object.getPrototypeOf(probe.store);
    if (proto[WRAPPED]) return;
    proto[WRAPPED] = true;
    core.wrap(proto, targets);
    const add = proto.addResourceBundle;
    proto.addResourceBundle = function (lng, ns, resources) {
      // a bundle added to one already there holds both files' keys
      const before = filesOf(this, lng, ns);
      const result = add.apply(this, arguments);
      const added = match(resources, lng, ns);
      byNsOf(this).set(`${lng}\u0000${ns}`, [...new Set([...before, ...added])]);
      return result;
    };
  };
  return { records, core, root, patch, parts: 0 };
}

/** The repository's root: PIPELINEGEN_LOCALE_ROOT, else the nearest directory above holding .git. */
function findRoot() {
  if (process.env.PIPELINEGEN_LOCALE_ROOT) return resolve(process.env.PIPELINEGEN_LOCALE_ROOT);
  for (let d = process.cwd(); ; d = dirname(d)) {
    if (existsSync(join(d, ".git"))) return d;
    if (dirname(d) === d) return process.cwd();
  }
}

function canonical(v) {
  if (Array.isArray(v)) return `[${v.map(canonical).join(",")}]`;
  if (v && typeof v === "object")
    return `{${Object.keys(v)
      .sort()
      .map((k) => `${JSON.stringify(k)}:${canonical(v[k])}`)
      .join(",")}}`;
  return JSON.stringify(v);
}

const catalogDirs = new Set(["locales", "locale", "i18n", "lang", "langs", "translations"]);
const skipDirs = new Set(["node_modules", ".git", "dist", "coverage"]);
const secret =
  /^(\.env(\..*)?|.*\.(pem|key|p12|pfx|jks|keystore)|.*secret.*|.*token.*|.*password.*|.*passwd.*|credentials.*)$/i;

/** The JSON files under directories named for catalogs, by content, read once when first needed. */
function lazyIndex(root) {
  let built;
  return () => {
    if (built) return built;
    built = { byContent: new Map(), byKeys: new Map(), keys: new Set() };
    const walk = (dir, inCatalog) => {
      let entries;
      try {
        entries = readdirSync(dir, { withFileTypes: true });
      } catch {
        return;
      }
      for (const e of entries) {
        const p = join(dir, e.name);
        if (e.isDirectory()) {
          if (skipDirs.has(e.name) || e.name.startsWith("bazel-") || secret.test(e.name)) continue;
          walk(p, inCatalog || catalogDirs.has(e.name.toLowerCase()));
        } else if (inCatalog && e.isFile() && e.name.endsWith(".json") && !secret.test(e.name)) {
          if (statSync(p).size > 32 * 1024 * 1024) continue;
          let v;
          try {
            v = JSON.parse(readFileSync(p, "utf8"));
          } catch {
            continue;
          }
          const rel = relative(root, p).split(sep).join(posix.sep);
          const c = canonical(v);
          built.byContent.set(c, [...(built.byContent.get(c) ?? []), rel]);
          const k = keySignature(v);
          built.byKeys.set(k, [...(built.byKeys.get(k) ?? []), rel]);
          collectKeys(v, "", built.keys);
        }
      }
    };
    walk(root, false);
    return built;
  };
}

/** A value's key paths, sorted, as one string: its shape without its texts. */
function keySignature(v) {
  const keys = new Set();
  collectKeys(v, "", keys);
  return [...keys].sort().join("\n");
}

function collectKeys(v, at, keys) {
  if (v && typeof v === "object")
    for (const k of Object.keys(v)) {
      const p = at ? `${at}.${k}` : k;
      keys.add(p);
      collectKeys(v[k], p, keys);
    }
}

function sharesKeys(bundle, keys) {
  const mine = new Set();
  collectKeys(bundle, "", mine);
  for (const k of mine) if (keys.has(k)) return true;
  return false;
}
