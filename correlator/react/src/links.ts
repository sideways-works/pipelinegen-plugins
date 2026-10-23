import { existsSync } from "node:fs";
import { join } from "node:path";
// Links from a unit to the units it reads, the npm packages it loads, and the stylesheets and plain
// files it imports, from user to used.
import {
  type FileSummary,
  isScript,
  isStyle,
  readTree,
  type Summary,
  scriptId,
  styleId,
} from "@sideways-works/ts-sbom";
import type { NarrowingName } from "./narrowings.ts";
import { type ResolveOptions, Resolver } from "./resolve.ts";

export interface Link {
  from: string;
  to: string;
  kind: string;
  /** With `while: "off"` the link stands only while that narrowing is off. */
  narrowing?: string;
  while?: "off" | "on";
}

export interface LinkSet {
  group: string;
  /** Files of the tree the set was read from. */
  inputs: string[];
  /** Paths its imports tried and found absent: a file appearing there can change where an import leads. */
  absent: string[];
  whole: false;
  links: Link[];
  cannot_tell: CannotTell[];
}

export interface CannotTell {
  unit: string;
  reason: string;
}

export interface LinkOptions extends ResolveOptions {}

export function depId(pkg: string): string {
  return `dep:npm:${pkg}`;
}

export function fileId(path: string): string {
  return `file:${path}`;
}

type ExportTarget =
  | { unit: string; executable: boolean }
  | { module: string }
  | { pkg: string }
  | { asset: string }
  | { none: true };

export class Linker {
  readonly summaries: Map<string, Summary>;
  private readonly resolver: Resolver;
  private readonly executable = new Map<string, boolean>();
  /** Files read while working out the current file's links. */
  private consulted = new Set<string>();
  /** Paths the current file's imports tried and found absent. */
  private absent = new Set<string>();
  /** Files every answer depends on: the ones import resolution reads. */
  private readonly shared: string[];
  /** The claimed files: a set's inputs are among them. */
  private readonly files: Set<string>;

  readonly root: string;
  readonly opt: LinkOptions;

  /** `files` are the claimed files, the only ones links reads or resolves to. */
  constructor(root: string, files: string[], opt: LinkOptions = {}, cache?: string) {
    this.root = root;
    this.opt = opt;
    this.shared = files.filter(
      (f) =>
        /(?:^|\/)(?:tsconfig[^/]*\.json|package\.json)$/.test(f) &&
        !f.split("/").includes("node_modules"),
    );
    this.files = new Set(files);
    this.resolver = new Resolver(root, files, opt);
    this.summaries = readTree(root, files, cache);
    for (const [file, s] of this.summaries)
      if (!("css" in s))
        for (const u of s.units) this.executable.set(scriptId(file, u.key), u.executable);
  }

  private script(file: string): FileSummary | undefined {
    this.consulted.add(file);
    const s = this.summaries.get(file);
    return s && !("css" in s) ? s : undefined;
  }

  exportOf(file: string, name: string, seen = new Set<string>()): ExportTarget | undefined {
    if (!isScript(file)) return { asset: file };
    const s = this.script(file);
    if (!s) return { asset: file };
    const k = `${file}\0${name}`;
    if (seen.has(k)) return undefined;
    seen.add(k);
    if (name === "*") return { module: file };
    for (const e of s.exports) {
      if (e.name !== name) continue;
      if (e.key) {
        const id = scriptId(file, e.key);
        return { unit: id, executable: this.executable.get(id) ?? true };
      }
      if (e.from !== undefined) return this.follow(e.from, file, e.imported ?? name, seen);
      return undefined;
    }
    for (const spec of s.star) {
      const r = this.follow(spec, file, name, seen);
      if (r && !("none" in r)) return r;
    }
    return undefined;
  }

  private follow(
    spec: string,
    from: string,
    name: string,
    seen: Set<string>,
  ): ExportTarget | undefined {
    const t = this.resolver.resolve(spec, from);
    if ("pkg" in t) return { pkg: t.pkg };
    if ("none" in t) return { none: true };
    // An import nothing resolves is under cannot_tell, with why, already.
    if ("missing" in t || "alias" in t) return undefined;
    return this.exportOf(t.file, name, seen);
  }

  links(): { links: Link[]; cannot_tell: CannotTell[] } {
    const sets = this.sets();
    return { links: sets.flatMap((x) => x.links), cannot_tell: sets.flatMap((x) => x.cannot_tell) };
  }

  /**
   * One set per file, wide and narrow alike: a link that exists only while a narrowing is off carries
   * its name and `while: "off"`, so one answer serves every level.
   */
  sets(paths?: string[]): LinkSet[] {
    const wide = (_: NarrowingName) => true;
    let out = new Map<string, Link>();
    let cannot: CannotTell[] = [];
    const add = (from: string, to: string, kind: string, narrowing?: string) => {
      if (from === to) return;
      const k = `${from}\0${to}`;
      const had = out.get(k);
      // A link that holds without a narrowing wins over one that exists only for it.
      if (had && (had.narrowing === undefined || narrowing !== undefined)) return;
      out.set(k, narrowing ? { from, to, kind, narrowing, while: "off" } : { from, to, kind });
    };
    const toTarget = (
      from: string,
      t: ExportTarget | undefined,
      spec: string,
      narrowExec?: NarrowingName,
    ) => {
      if (!t) {
        cannot.push({ unit: from, reason: `reads a name of ${spec} that it does not export` });
        return;
      }
      if ("none" in t) return;
      if ("pkg" in t) add(from, depId(t.pkg), "uses");
      else if ("asset" in t)
        add(from, isStyle(t.asset) ? styleId(t.asset) : fileId(t.asset), "reads");
      else if ("module" in t) {
        // The whole module through a namespace: its data always, its functions when wide.
        const s = this.script(t.module);
        for (const u of s?.units ?? []) {
          if (u.kind === "type") continue;
          if (!u.executable) add(from, scriptId(t.module, u.key), "reads");
          else if (narrowExec && wide(narrowExec))
            add(from, scriptId(t.module, u.key), "uses", narrowExec);
        }
      } else if (!t.executable) add(from, t.unit, "reads");
      else if (narrowExec && wide(narrowExec)) add(from, t.unit, "uses", narrowExec);
    };
    const sets: LinkSet[] = [];
    // Per set, the absent paths its imports tried, listed apart from the inputs.
    const probed = new Map<string, Set<string>>();

    for (const [file, s] of this.summaries) {
      if ("css" in s) continue;
      out = new Map();
      cannot = [];
      this.consulted = new Set([file]);
      this.absent = new Set();
      this.resolver.absent = this.absent;
      const kinds = new Map(s.units.map((u) => [u.key, u]));
      const imports = new Map<string, { specifier: string; imported: string }>();
      const typeImports = new Map<string, { specifier: string; imported: string }>();
      for (const i of s.imports)
        for (const n of i.names) {
          const m = i.kind === "static" ? imports : i.kind === "type" ? typeImports : undefined;
          m?.set(n.local, { specifier: i.specifier, imported: n.imported });
        }
      const typeSide = new Map([...imports, ...typeImports]);
      const init = scriptId(file, "init");
      const hasInit = kinds.has("init");

      for (const u of s.units) {
        const id = scriptId(file, u.key);
        if (u.kind === "type") {
          // A type reads other types: only followed when type edits bind tests.
          if (wide("types-erased"))
            for (const r of u.refs)
              this.refLink(file, s, id, r, typeSide, kinds, add, toTarget, "types-erased", true);
          continue;
        }
        for (const r of u.refs)
          this.refLink(file, s, id, r, imports, kinds, add, toTarget, "ts-functions", false);
        if (wide("types-erased"))
          for (const r of u.typeRefs ?? [])
            this.refLink(file, s, id, r, typeSide, kinds, add, toTarget, "types-erased", true);
        // Wide: a unit that ran binds its whole file, through the module's init.
        if (wide("ts-functions") && hasInit && u.key !== "init") {
          add(id, init, "uses", "ts-functions");
          add(init, id, "uses", "ts-functions");
        }
      }

      for (const i of s.imports) {
        if (i.kind === "type") continue;
        const from = i.in ? scriptId(file, i.in) : hasInit ? init : undefined;
        if (!from) continue;
        const t = this.resolver.resolve(i.specifier, file);
        if ("none" in t) continue;
        if ("missing" in t) {
          cannot.push({
            unit: from,
            reason: `imports ${i.specifier}, which names no file of the tree`,
          });
          continue;
        }
        if ("alias" in t) {
          cannot.push({
            unit: from,
            reason: `import '${i.specifier}' matches no package name and no tsconfig path: declare the alias under the plugin's config \`aliases\` (a bundler-only alias such as Vite's resolve.alias or esbuild's --alias)`,
          });
          continue;
        }
        if ("pkg" in t) {
          // Loading a package runs its own top-level code.
          add(from, depId(t.pkg), "uses");
          continue;
        }
        if (i.kind === "worker" || /[?&](?:worker|sharedworker)\b/.test(i.specifier)) {
          // Coverage never sees a worker's code: whoever starts it binds all of it.
          const users = [from];
          if (i.kind === "static")
            for (const u of s.units)
              if (u.refs.some((r) => i.names.some((n) => n.local === r.split(".")[0])))
                users.push(scriptId(file, u.key));
          if (isScript(t.file))
            for (const to of this.workerUnits(t.file)) for (const f of users) add(f, to, "uses");
          continue;
        }
        if (isStyle(t.file)) add(from, styleId(t.file), "uses");
        else if (!isScript(t.file)) add(from, fileId(t.file), "reads");
        else if (i.kind === "dynamic" || i.kind === "require") {
          // What the loaded module runs on load is seen by coverage; wide, bind it.
          if (wide("ts-functions") && this.script(t.file)?.units.some((x) => x.key === "init"))
            add(from, scriptId(t.file, "init"), "uses", "ts-functions");
        }
      }
      const links = [...out.values()].sort((a, b) =>
        a.from < b.from ? -1 : a.from > b.from ? 1 : a.to < b.to ? -1 : a.to > b.to ? 1 : 0,
      );
      sets.push({
        group: file,
        inputs: [...new Set([...this.consulted, ...this.shared])]
          .filter((f) => this.files.has(f))
          .sort(),
        // A probe that names a directory or an unclaimed file keys nothing.
        absent: [...this.absent]
          .filter((f) => !this.files.has(f) && !existsSync(join(this.root, f)))
          .sort(),
        whole: false,
        links,
        cannot_tell: cannot,
      });
      probed.set(file, this.absent);
    }
    this.resolver.absent = undefined;
    if (!paths) return sets;
    const want = new Set(paths);
    const answered = sets.filter(
      (x) =>
        x.inputs.some((i) => want.has(i)) ||
        [...(probed.get(x.group) ?? [])].some((a) => want.has(a)),
    );
    // A script that is gone answers as an empty set.
    for (const p of paths)
      if (isScript(p) && !this.summaries.has(p))
        answered.push({
          group: p,
          inputs: [],
          absent: [p],
          whole: false,
          links: [],
          cannot_tell: [],
        });
    return answered;
  }

  private workerUnits(entry: string): string[] {
    const out = new Set<string>();
    const seen = new Set<string>();
    const queue = [entry];
    while (queue.length) {
      const f = queue.pop() as string;
      if (seen.has(f)) continue;
      seen.add(f);
      const s = this.script(f);
      if (!s) continue;
      for (const u of s.units) if (u.kind !== "type") out.add(scriptId(f, u.key));
      for (const i of s.imports) {
        if (i.kind === "type") continue;
        const t = this.resolver.resolve(i.specifier, f);
        if ("pkg" in t) out.add(depId(t.pkg));
        else if ("file" in t) {
          if (isScript(t.file)) queue.push(t.file);
          else out.add(isStyle(t.file) ? styleId(t.file) : fileId(t.file));
        }
      }
    }
    return [...out].sort();
  }

  private refLink(
    file: string,
    s: FileSummary,
    from: string,
    ref: string,
    imports: Map<string, { specifier: string; imported: string }>,
    kinds: Map<string, FileSummary["units"][number]>,
    add: (from: string, to: string, kind: string, narrowing?: string) => void,
    toTarget: (
      from: string,
      t: ExportTarget | undefined,
      spec: string,
      narrowExec?: NarrowingName,
    ) => void,
    narrowing: NarrowingName,
    typeOnly: boolean,
  ): void {
    const [base, member] = ref.split(".") as [string, string | undefined];
    const localKey = s.locals[base];
    if (localKey && !imports.has(base)) {
      const t = kinds.get(localKey);
      if (!t) return;
      const to = scriptId(file, localKey);
      if (typeOnly) add(from, to, "reads", narrowing);
      else if (!t.executable) add(from, to, "reads");
      else add(from, to, "uses", narrowing);
      return;
    }
    const imp = imports.get(base);
    if (!imp) return;
    const name = imp.imported === "*" && member ? member : imp.imported;
    const target = this.follow(imp.specifier, file, name, new Set());
    if (typeOnly) {
      if (target && "unit" in target) add(from, target.unit, "reads", narrowing);
      return;
    }
    toTarget(from, target, imp.specifier, narrowing);
  }
}
