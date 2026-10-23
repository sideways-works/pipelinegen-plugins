// Where an import specifier leads: a file of the tree, an npm package, or nothing this correlator
// can follow. Resolution reads only the claimed files, never node_modules. It records the paths it
// tried and found absent too: `./x` is `x.ts` only while no file `x` exists.
import { readFileSync } from "node:fs";
import { builtinModules } from "node:module";
import { join, posix } from "node:path";
import ts from "typescript";

export type Target =
  | { file: string }
  | { pkg: string }
  | { none: string }
  /** a relative or aliased path that names no file of the tree */
  | { missing: string }
  /** a bare specifier no package name can be: an alias nothing here declares */
  | { alias: string };

export interface ResolveOptions {
  /** Prefix aliases the bundler applies, such as `{"@ui": "packages/ui/src"}`. */
  aliases?: Record<string, string>;
}

interface PathsConfig {
  dir: string; // directory the paths are relative to
  paths: Record<string, string[]>;
  /** Only a baseUrl, no paths: a bare specifier tries the base first, then is a package. */
  baseOnly?: boolean;
}

interface Project {
  file: string;
  config: PathsConfig | null;
  include: RegExp[];
  exclude: RegExp[];
  references: string[];
}

interface Workspace {
  dir: string;
  exports: Record<string, string>; // subpath ("." or "./x") → file in the tree
}

const builtins = new Set(builtinModules.flatMap((m) => [m, `node:${m}`]));
const probes = ["", ".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"];
const indexProbes = ["/index.ts", "/index.tsx", "/index.js", "/index.jsx", "/index.mjs"];

/** `@/x`, `~/x` and `#x` can't name a package. */
export function packageLike(spec: string): boolean {
  const name = packageName(spec);
  return /^(?:@[a-z0-9][a-z0-9._~-]*\/)?[a-z0-9][a-z0-9._~-]*$/i.test(name);
}

function pattern(dir: string, p: string): RegExp {
  let glob = posix.normalize(posix.join(dir, p)).replace(/^\.\//, "");
  // A pattern without a wildcard or an extension names a directory.
  if (!/[*?]/.test(glob) && !/\.[a-z]+$/i.test(glob)) glob += "/**/*";
  let re = "";
  for (let i = 0; i < glob.length; i++) {
    const c = glob[i] as string;
    if (c === "*" && glob[i + 1] === "*") {
      re += glob[i + 2] === "/" ? "(?:.*/)?" : ".*";
      i += glob[i + 2] === "/" ? 2 : 1;
    } else if (c === "*") re += "[^/]*";
    else if (c === "?") re += "[^/]";
    else re += c.replace(/[.+^${}()|[\]\\]/g, "\\$&");
  }
  return new RegExp(`^${re}$`);
}

/** `@scope/pkg/sub` is `@scope/pkg`. */
export function packageName(spec: string): string {
  const parts = spec.split("/");
  return spec.startsWith("@") ? parts.slice(0, 2).join("/") : (parts[0] ?? spec);
}

export class Resolver {
  private readonly files: Set<string>;
  private readonly tsconfigs = new Map<string, { config: PathsConfig | null; absent: string[] }>();
  private readonly workspaces = new Map<string, Workspace>();
  private readonly cache = new Map<string, { target: Target; absent: string[] }>();
  /** Receives the absent paths of every resolution while it is set. */
  absent: Set<string> | undefined;

  private readonly root: string;
  private readonly opt: ResolveOptions;

  constructor(root: string, files: Iterable<string>, opt: ResolveOptions = {}) {
    this.root = root;
    this.opt = opt;
    this.files = new Set(files);
    this.readWorkspaces();
  }

  private read(path: string): string | undefined {
    // A file that wasn't claimed is not there for resolution.
    if (!this.files.has(path)) return undefined;
    try {
      return readFileSync(join(this.root, path), "utf8");
    } catch {
      return undefined;
    }
  }

  private readWorkspaces(): void {
    for (const f of this.files) {
      if (!f.endsWith("package.json") || f.split("/").includes("node_modules")) continue;
      const text = this.read(f);
      if (!text) continue;
      let pkg: { name?: string; main?: string; module?: string; exports?: unknown };
      try {
        pkg = JSON.parse(text);
      } catch {
        continue;
      }
      if (!pkg.name) continue;
      const dir = posix.dirname(f);
      const exports: Record<string, string> = {};
      const pick = (v: unknown): string | undefined => {
        if (typeof v === "string") return v;
        if (v && typeof v === "object") {
          const o = v as Record<string, unknown>;
          for (const k of [
            "source",
            "import",
            "module",
            "browser",
            "default",
            "require",
            "types",
          ]) {
            const p = pick(o[k]);
            if (p) return p;
          }
        }
        return undefined;
      };
      const ex = pkg.exports;
      if (typeof ex === "string") exports["."] = ex;
      else if (ex && typeof ex === "object") {
        const keys = Object.keys(ex);
        if (keys.some((k) => k.startsWith("."))) {
          for (const k of keys) {
            const p = pick((ex as Record<string, unknown>)[k]);
            if (p) exports[k] = p;
          }
        } else {
          const p = pick(ex);
          if (p) exports["."] = p;
        }
      }
      if (!exports["."]) {
        const main = pkg.module ?? pkg.main;
        if (main) exports["."] = main;
      }
      this.workspaces.set(pkg.name, { dir: dir === "." ? "" : dir, exports });
    }
  }

  private pathsFor(dir: string): { config: PathsConfig | null; absent: string[] } {
    const hit = this.tsconfigs.get(dir);
    if (hit) return hit;
    const own = posix.join(dir, "tsconfig.json");
    const absent: string[] = [];
    let r: { config: PathsConfig | null; absent: string[] };
    if (this.files.has(own)) r = { config: this.loadTsconfig(own, 0, absent), absent };
    else if (dir === "." || dir === "") r = { config: null, absent: [own] };
    else {
      const up = this.pathsFor(posix.dirname(dir));
      r = { config: up.config, absent: [own, ...up.absent] };
    }
    this.tsconfigs.set(dir, r);
    return r;
  }

  private loadTsconfig(file: string, depth: number, absent: string[]): PathsConfig | null {
    if (depth > 8) return null;
    const text = this.read(file);
    if (text === undefined) {
      absent.push(file);
      return null;
    }
    const parsed = ts.parseConfigFileTextToJson(file, text);
    const cfg = parsed.config as
      | {
          extends?: string | string[];
          compilerOptions?: { paths?: Record<string, string[]>; baseUrl?: string };
        }
      | undefined;
    if (!cfg) return null;
    const dir = posix.dirname(file);
    const co = cfg.compilerOptions ?? {};
    if (co.paths) return { dir: posix.join(dir, co.baseUrl ?? "."), paths: co.paths };
    if (co.baseUrl) return { dir: posix.join(dir, co.baseUrl), paths: {}, baseOnly: true };
    const ext = Array.isArray(cfg.extends) ? cfg.extends : cfg.extends ? [cfg.extends] : [];
    for (const e of ext.reverse()) {
      if (!e.startsWith(".")) continue; // a shared config from a package: no paths of this tree
      let p = posix.normalize(posix.join(dir, e));
      if (!p.endsWith(".json")) p += ".json";
      const r = this.loadTsconfig(p, depth + 1, absent);
      if (r) return r;
    }
    return null;
  }

  /** Tries the extensions a bundler tries; misses go into `absent`. */
  private probe(path: string, absent: string[]): string | undefined {
    const p = posix.normalize(path);
    const tried: string[] = [];
    for (const e of probes) tried.push(p + e);
    // TypeScript lets `./x.js` name `./x.ts`.
    const m = /\.(m|c)?jsx?$/.exec(p);
    if (m) {
      const stem = p.slice(0, m.index);
      for (const e of [".ts", ".tsx", ".mts", ".cts"]) tried.push(stem + e);
    }
    for (const e of indexProbes) tried.push(p + e);
    for (const c of tried) {
      if (this.files.has(c)) return c;
      absent.push(c);
    }
    return undefined;
  }

  resolve(spec: string, from: string): Target {
    // Files of one directory can belong to different projects of a solution tsconfig.
    const key = `${this.projectOf(from)?.file ?? ""}\0${posix.dirname(from)}\0${spec}`;
    let hit = this.cache.get(key);
    if (!hit) {
      const absent: string[] = [];
      hit = { target: this.resolveUncached(spec, from, absent), absent };
      this.cache.set(key, hit);
    }
    if (this.absent) for (const a of hit.absent) this.absent.add(a);
    return hit.target;
  }

  private resolveUncached(raw: string, from: string, absent: string[]): Target {
    // `?raw`, `?url`, `?worker`: still the same file.
    const spec = raw.replace(/[?#].*$/, "");
    if (spec === "" || raw.startsWith("\0") || spec.startsWith("virtual:")) return { none: raw };
    if (builtins.has(spec)) return { none: raw };
    if (spec.startsWith("./") || spec.startsWith("../") || spec === "." || spec === "..") {
      const f = this.probe(posix.join(posix.dirname(from), spec), absent);
      return f ? { file: f } : { missing: raw };
    }
    if (spec.startsWith("/")) {
      const f = this.probe(spec.slice(1), absent);
      return f ? { file: f } : { missing: raw };
    }
    for (const [alias, target] of Object.entries(this.opt.aliases ?? {})) {
      if (spec === alias || spec.startsWith(`${alias}/`)) {
        const f = this.probe(posix.join(target, spec.slice(alias.length)), absent);
        return f ? { file: f } : { missing: raw };
      }
    }
    const found = this.pathsFor(posix.dirname(from));
    absent.push(...found.absent);
    const pc = this.projectOf(from)?.config ?? found.config;
    if (pc && !pc.baseOnly) {
      for (const [pattern, targets] of Object.entries(pc.paths)) {
        const star = pattern.indexOf("*");
        let rest: string | undefined;
        if (star < 0) rest = spec === pattern ? "" : undefined;
        else {
          const pre = pattern.slice(0, star);
          const post = pattern.slice(star + 1);
          if (
            spec.startsWith(pre) &&
            spec.endsWith(post) &&
            spec.length >= pre.length + post.length
          )
            rest = spec.slice(pre.length, spec.length - post.length);
        }
        if (rest === undefined) continue;
        for (const t of targets) {
          const f = this.probe(posix.join(pc.dir, t.replace("*", rest)), absent);
          if (f) return { file: f };
        }
        return { missing: raw };
      }
    }
    if (pc?.baseOnly) {
      // With a baseUrl, TypeScript tries the base before node_modules.
      const f = this.probe(posix.join(pc.dir, spec), absent);
      if (f) return { file: f };
    }
    const name = packageName(spec);
    const ws = this.workspaces.get(name);
    if (ws) {
      const sub = spec === name ? "." : `.${spec.slice(name.length)}`;
      const target = ws.exports[sub];
      if (target) {
        const f = this.probe(posix.join(ws.dir, target), absent);
        if (f) return { file: f };
      }
      const f = this.probe(posix.join(ws.dir, sub), absent);
      return f ? { file: f } : { missing: raw };
    }
    if (!packageLike(spec)) return { alias: raw };
    return { pkg: name };
  }

  private readonly projects = new Map<string, Project | null>();
  private readonly projectOfFile = new Map<string, Project | null>();

  /**
   * For a solution tsconfig (`references`, no settings of its own): the referenced project whose
   * include covers the file and whose exclude doesn't, as TypeScript picks it; else the first with
   * `paths`.
   */
  private projectOf(file: string): Project | null {
    const hit = this.projectOfFile.get(file);
    if (hit !== undefined) return hit;
    let out: Project | null = null;
    for (let dir = posix.dirname(file); ; dir = posix.dirname(dir)) {
      const own = posix.join(dir === "" ? "." : dir, "tsconfig.json").replace(/^\.\//, "");
      if (this.files.has(own)) {
        const p = this.project(own);
        if (p?.references.length && !p.config) {
          const refs = p.references.map((r) => this.project(r)).filter((r): r is Project => !!r);
          out =
            refs.find(
              (r) =>
                r.include.some((re) => re.test(file)) && !r.exclude.some((re) => re.test(file)),
            ) ??
            refs.find((r) => r.config) ??
            null;
        }
        break;
      }
      if (dir === "." || dir === "" || dir === "/") break;
    }
    this.projectOfFile.set(file, out);
    return out;
  }

  private project(file: string): Project | null {
    const hit = this.projects.get(file);
    if (hit !== undefined) return hit;
    let out: Project | null = null;
    const text = this.read(file);
    if (text !== undefined) {
      const cfg = ts.parseConfigFileTextToJson(file, text).config as
        | {
            include?: string[];
            files?: string[];
            exclude?: string[];
            references?: { path: string }[];
          }
        | undefined;
      const dir = posix.dirname(file);
      const refs = (cfg?.references ?? []).map((r) => {
        const p = posix.normalize(posix.join(dir, r.path));
        return p.endsWith(".json") ? p : posix.join(p, "tsconfig.json");
      });
      const include =
        cfg?.files || cfg?.include ? [...(cfg.files ?? []), ...(cfg.include ?? [])] : ["**/*"];
      out = {
        file,
        config: this.loadTsconfig(file, 0, []),
        include: include.map((i) => pattern(dir, i)),
        exclude: (cfg?.exclude ?? []).map((i) => pattern(dir, i)),
        references: refs,
      };
    }
    this.projects.set(file, out);
    return out;
  }
}
