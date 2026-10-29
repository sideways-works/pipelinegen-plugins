// The recorder's core: it wraps an i18next resource store so every probe,
// misses included, becomes one record. It runs in Node and in the browser
// (an init script built from this function's source), so it uses nothing but
// the language. i18next probes the store for every key form, fallback
// language and namespace it tries, which is exactly what a text depends on.
export function localeKeysCore(sink) {
  const seen = new Set();
  const put = (r) => {
    const s = JSON.stringify(r);
    if (seen.has(s)) return;
    seen.add(s);
    sink(r);
  };
  const patched = new WeakSet();

  // a key's parts joined by dots, as locale-sbom names a key
  const norm = (key, sep) => {
    if (Array.isArray(key)) return key.map(String).join(".");
    if (typeof key !== "string") return null;
    if (typeof sep === "string" && sep !== "" && sep !== ".") return key.split(sep).join(".");
    return key;
  };

  // one probe of the store: targets(store, lng, ns) names the catalog
  // records it stands for ({c, l, n} or {f})
  const probe = (store, targets, lng, ns, key, options, presence) => {
    if (typeof lng === "string" && lng.includes(".") && ns === undefined && key === undefined) {
      const parts = lng.split(".");
      lng = parts[0];
      ns = parts[1];
      key = parts.length > 2 ? parts.slice(2).join(".") : undefined;
    }
    if (typeof lng !== "string" || typeof ns !== "string") return;
    const sep =
      options && options.keySeparator !== undefined
        ? options.keySeparator
        : store.options && store.options.keySeparator !== undefined
          ? store.options.keySeparator
          : ".";
    for (const t of targets(store, lng, ns)) {
      if (presence || key === undefined || key === null || key === "") {
        put(presence ? { ...t, has: true } : { ...t, k: "" });
        continue;
      }
      const k = norm(key, sep);
      if (k === null) put({ cant: `a lookup with a key of type ${typeof key}` });
      else put({ ...t, k });
    }
  };

  const wrap = (target, targets) => {
    if (patched.has(target)) return;
    patched.add(target);
    const get = target.getResource;
    const has = target.hasResourceBundle;
    const byLang = target.getDataByLanguage;
    let checking = 0;
    target.getResource = function (lng, ns, key, options) {
      const value = get.apply(this, arguments);
      try {
        probe(this, targets, lng, ns, key, options, checking > 0);
      } catch (e) {
        put({ cant: `recording a lookup failed: ${e}` });
      }
      return value;
    };
    if (typeof has === "function") {
      target.hasResourceBundle = function () {
        checking++;
        try {
          return has.apply(this, arguments);
        } finally {
          checking--;
        }
      };
    }
    if (typeof byLang === "function") {
      // i18next reads a language's bundles to see that it has any: a
      // check that each is there, not a read of their keys
      target.getDataByLanguage = function (lng) {
        const data = byLang.apply(this, arguments);
        try {
          for (const ns of Object.keys(data || {})) probe(this, targets, lng, ns, undefined, undefined, true);
        } catch (e) {
          put({ cant: `recording a lookup failed: ${e}` });
        }
        return data;
      };
    }
  };

  // install(i18n, catalog): every lookup of this instance is one of the
  // catalog's (its directory from the repository's root), each language
  // and namespace named as the catalog's files are
  const install = (i18n, catalog) => {
    if (typeof catalog !== "string" || catalog === "") {
      put({ cant: "the app's i18n hook was called without the catalog's directory" });
      return;
    }
    put({ c: catalog, load: true });
    const targets = (_store, lng, ns) => [{ c: catalog, l: lng, n: ns }];
    if (i18n.store) {
      wrap(i18n.store, targets);
      return;
    }
    // init assigns the store: wrap it then, before the first lookup
    let store;
    Object.defineProperty(i18n, "store", {
      configurable: true,
      enumerable: true,
      get: () => store,
      set: (s) => {
        store = s;
        if (s) wrap(s, targets);
      },
    });
  };

  const reset = () => seen.clear();

  return { install, wrap, put, reset };
}
