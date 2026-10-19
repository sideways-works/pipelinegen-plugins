// Units inside one file: top-level functions, components, classes and their methods, constants,
// types, and the module's own top-level code. They depend on the file's text alone, so a summary
// can be cached by content.
import { createHash } from "node:crypto";
import ts from "typescript";
import { canon, isTypeOnly, jsxPragmas } from "./canon.ts";
import { incomplete } from "./complete.ts";

const K = ts.SyntaxKind;

export type UnitKind = "fn" | "method" | "class" | "const" | "type" | "init";

export interface FileUnit {
  /** `fn:Name`, `method:Class.name`, `init`. */
  key: string;
  kind: UnitKind;
  /** Whether coverage can see it run. Types and plain data can't. */
  executable: boolean;
  digest: string;
  /** UTF-16 offsets [start, end). */
  ranges: [number, number][];
  /** Top-level names and import bindings it reads (`ns.name` through a namespace import). */
  refs: string[];
  /** Names used only in types, which the build erases. */
  typeRefs?: string[];
}

/** `type`: erased by the build. `worker`: a module started with `new Worker(...)`. */
export type ImportKind =
  | "static"
  | "side-effect"
  | "dynamic"
  | "re-export"
  | "require"
  | "type"
  | "worker";

export interface FileImport {
  specifier: string;
  kind: ImportKind;
  /** local name → the name in the imported module (`default`, `*`). */
  names: { local: string; imported: string }[];
  /** The unit holding a dynamic import or require. */
  in?: string;
}

export interface FileExport {
  name: string;
  key?: string;
  /** Or a name of another module, passed through. */
  from?: string;
  imported?: string;
}

export interface FileSummary {
  units: FileUnit[];
  imports: FileImport[];
  exports: FileExport[];
  /** `export * from` specifiers. */
  star: string[];
  locals: Record<string, string>;
  /** Syntax errors: the file's units are still found, but less is known. */
  errors: number;
  /** Why the units don't hold everything a test can observe of the file; absent when they do. */
  incomplete?: string;
}

export function scriptKind(path: string): ts.ScriptKind {
  if (path.endsWith(".tsx")) return ts.ScriptKind.TSX;
  if (/\.[mc]?ts$/.test(path)) return ts.ScriptKind.TS;
  if (path.endsWith(".jsx")) return ts.ScriptKind.JSX;
  return ts.ScriptKind.JS;
}

export function parse(path: string, text: string): ts.SourceFile {
  return ts.createSourceFile(path, text, ts.ScriptTarget.Latest, true, scriptKind(path));
}

export function hash(parts: string[]): string {
  const h = createHash("sha256");
  for (const p of parts) h.update(p);
  return h.digest("hex").slice(0, 32);
}

function strip(e: ts.Expression): ts.Expression {
  while (
    ts.isParenthesizedExpression(e) ||
    ts.isAsExpression(e) ||
    ts.isSatisfiesExpression(e) ||
    ts.isNonNullExpression(e) ||
    ts.isTypeAssertionExpression(e)
  )
    e = e.expression;
  return e;
}

function isFunctionValue(e: ts.Expression | undefined, depth = 0): boolean {
  if (!e) return false;
  e = strip(e);
  if (ts.isArrowFunction(e) || ts.isFunctionExpression(e)) return true;
  if (ts.isCallExpression(e) && depth < 3)
    return e.arguments.some((a) => isFunctionValue(a, depth + 1));
  return false;
}

function containsFunction(n: ts.Node): boolean {
  let found = false;
  const visit = (c: ts.Node): void => {
    if (found) return;
    if (ts.isFunctionLike(c) && !ts.isTypeNode(c) && "body" in c && c.body) {
      found = true;
      return;
    }
    if (ts.isClassLike(c)) {
      found = true;
      return;
    }
    ts.forEachChild(c, visit);
  };
  visit(n);
  return found;
}

function bindingNames(name: ts.BindingName, out: string[] = []): string[] {
  if (ts.isIdentifier(name)) out.push(name.text);
  else for (const el of name.elements) if (ts.isBindingElement(el)) bindingNames(el.name, out);
  return out;
}

function memberName(m: ts.ClassElement, sf: ts.SourceFile): string {
  const n = m.name;
  let base: string;
  if (!n) base = ts.isConstructorDeclaration(m) ? "constructor" : "static{}";
  else if (ts.isIdentifier(n) || ts.isPrivateIdentifier(n)) base = n.text;
  else if (ts.isStringLiteral(n) || ts.isNumericLiteral(n)) base = n.text;
  else base = n.getText(sf).replace(/\s+/g, "");
  if (ts.isGetAccessorDeclaration(m)) base = `get ${base}`;
  if (ts.isSetAccessorDeclaration(m)) base = `set ${base}`;
  const isStatic =
    ts.canHaveModifiers(m) && ts.getModifiers(m)?.some((x) => x.kind === K.StaticKeyword);
  return isStatic ? `static ${base}` : base;
}

function isMethodLike(m: ts.ClassElement): boolean {
  if (ts.isMethodDeclaration(m) || ts.isGetAccessorDeclaration(m) || ts.isSetAccessorDeclaration(m))
    return m.body !== undefined;
  if (ts.isConstructorDeclaration(m)) return m.body !== undefined;
  if (ts.isPropertyDeclaration(m)) return isFunctionValue(m.initializer);
  return false;
}

/** The module a `new Worker(...)` argument names: a string, or `new URL(string, import.meta.url)`. */
function workerSpec(a: ts.Expression | undefined): string | undefined {
  if (!a) return undefined;
  if (ts.isStringLiteral(a) || ts.isNoSubstitutionTemplateLiteral(a)) return a.text;
  if (ts.isNewExpression(a) && ts.isIdentifier(a.expression) && a.expression.text === "URL") {
    const [first, second] = a.arguments ?? [];
    if (
      first &&
      (ts.isStringLiteral(first) || ts.isNoSubstitutionTemplateLiteral(first)) &&
      second &&
      ts.isPropertyAccessExpression(second) &&
      ts.isMetaProperty(second.expression)
    )
      return first.text;
  }
  return undefined;
}

function hasDeclare(n: ts.Node): boolean {
  return (
    ts.canHaveModifiers(n) &&
    (ts.getModifiers(n)?.some((m) => m.kind === K.DeclareKeyword) ?? false)
  );
}

interface Draft {
  kind: UnitKind;
  nodes: ts.Node[];
  ranges: [number, number][];
  skip?: Set<ts.Node>;
  executable: boolean;
}

export function summarize(path: string, text: string): FileSummary {
  const sf = parse(path, text);
  // JSX pragmas build every element in the file, so they are its init's.
  const pragmas = jsxPragmas(text);
  const drafts = new Map<string, Draft>();
  const local = new Map<string, string>(); // top-level name → unit key
  const imports: FileImport[] = [];
  const exports: FileExport[] = [];
  const star: string[] = [];
  const importLocals = new Map<string, { specifier: string; imported: string }>();
  const typeLocals = new Set<string>();
  const gaps: string[] = [];
  const told = new Set<number>();
  const gap = (why: string) => {
    if (!gaps.includes(why)) gaps.push(why);
  };
  const range = (n: ts.Node): [number, number] => [n.getStart(sf), n.end];

  const add = (
    key: string,
    kind: UnitKind,
    node: ts.Node,
    executable: boolean,
    r = range(node),
  ): Draft => {
    let d = drafts.get(key);
    if (!d) {
      d = { kind, nodes: [], ranges: [], executable: false };
      drafts.set(key, d);
    }
    d.nodes.push(node);
    d.ranges.push(r);
    d.executable ||= executable;
    return d;
  };
  const init = (node: ts.Node) => add("init", "init", node, true);
  const isExported = (n: ts.Node) =>
    ts.canHaveModifiers(n) &&
    (ts.getModifiers(n)?.some((m) => m.kind === K.ExportKeyword) ?? false);
  const isDefault = (n: ts.Node) =>
    ts.canHaveModifiers(n) &&
    (ts.getModifiers(n)?.some((m) => m.kind === K.DefaultKeyword) ?? false);

  for (const st of sf.statements) {
    if (ts.isImportDeclaration(st)) {
      const spec = (st.moduleSpecifier as ts.StringLiteral).text;
      const c = st.importClause;
      const typeNames: FileImport["names"] = [];
      if (c && isTypeOnly(st)) {
        if (c.name) typeNames.push({ local: c.name.text, imported: "default" });
        const tb = c.namedBindings;
        if (tb && ts.isNamespaceImport(tb)) typeNames.push({ local: tb.name.text, imported: "*" });
        if (tb && ts.isNamedImports(tb))
          for (const e of tb.elements)
            typeNames.push({ local: e.name.text, imported: (e.propertyName ?? e.name).text });
      } else if (c?.namedBindings && ts.isNamedImports(c.namedBindings))
        for (const e of c.namedBindings.elements)
          if (e.isTypeOnly)
            typeNames.push({ local: e.name.text, imported: (e.propertyName ?? e.name).text });
      if (typeNames.length) {
        imports.push({ specifier: spec, kind: "type", names: typeNames });
        for (const n of typeNames) typeLocals.add(n.local);
      }
      if (isTypeOnly(st)) continue;
      // `with { type: "json" }` and `import defer` change how the module loads; init holds only its
      // specifier.
      if (st.attributes || (c?.phaseModifier !== undefined && c.phaseModifier !== K.TypeKeyword))
        gap("an import's attributes or phase, which no digest holds");
      if (!c) {
        imports.push({ specifier: spec, kind: "side-effect", names: [] });
        init(st);
        continue;
      }
      const names: FileImport["names"] = [];
      if (c.name) names.push({ local: c.name.text, imported: "default" });
      const b = c.namedBindings;
      if (b && ts.isNamespaceImport(b)) names.push({ local: b.name.text, imported: "*" });
      if (b && ts.isNamedImports(b))
        for (const e of b.elements)
          if (!e.isTypeOnly)
            names.push({ local: e.name.text, imported: (e.propertyName ?? e.name).text });
      for (const n of names) importLocals.set(n.local, { specifier: spec, imported: n.imported });
      imports.push({ specifier: spec, kind: "static", names });
      continue;
    }
    if (ts.isExportDeclaration(st)) {
      if (isTypeOnly(st)) {
        // Types passed through, for following type uses.
        const spec = st.moduleSpecifier && (st.moduleSpecifier as ts.StringLiteral).text;
        const c = st.exportClause;
        if (spec !== undefined && c && ts.isNamedExports(c))
          for (const e of c.elements)
            exports.push({
              name: e.name.text,
              from: spec,
              imported: (e.propertyName ?? e.name).text,
            });
        continue;
      }
      const spec = st.moduleSpecifier && (st.moduleSpecifier as ts.StringLiteral).text;
      const c = st.exportClause;
      if (spec !== undefined) {
        const names: FileImport["names"] = [];
        if (!c) star.push(spec);
        else if (ts.isNamespaceExport(c)) {
          exports.push({ name: c.name.text, from: spec, imported: "*" });
          names.push({ local: c.name.text, imported: "*" });
        } else
          for (const e of c.elements)
            if (!e.isTypeOnly) {
              const imported = (e.propertyName ?? e.name).text;
              exports.push({ name: e.name.text, from: spec, imported });
              names.push({ local: e.name.text, imported });
            }
        imports.push({ specifier: spec, kind: "re-export", names });
      } else if (c && ts.isNamedExports(c)) {
        for (const e of c.elements)
          if (!e.isTypeOnly)
            exports.push({ name: e.name.text, key: `@${(e.propertyName ?? e.name).text}` });
      }
      init(st);
      continue;
    }
    // An overload signature: the implementation that follows is the unit.
    if (ts.isFunctionDeclaration(st) && !st.body && !hasDeclare(st)) continue;
    if (isTypeOnly(st)) {
      const name = (st as { name?: ts.Node }).name;
      if (name && (ts.isIdentifier(name) || ts.isStringLiteral(name))) {
        const key = `type:${name.text}`;
        add(key, "type", st, false);
        if (!local.has(name.text)) local.set(name.text, key);
        if (isExported(st)) exports.push({ name: name.text, key });
      }
      continue;
    }
    if (ts.isFunctionDeclaration(st)) {
      const name = st.name?.text ?? "default";
      const key = `fn:${name}`;
      add(key, "fn", st, true);
      if (st.name) local.set(name, key);
      if (isExported(st)) exports.push({ name: isDefault(st) ? "default" : name, key });
      continue;
    }
    if (ts.isClassDeclaration(st)) {
      const name = st.name?.text ?? "default";
      const key = `class:${name}`;
      const skip = new Set<ts.Node>();
      for (const m of st.members) {
        if (!isMethodLike(m) || isTypeOnly(m)) continue;
        skip.add(m);
        add(`method:${name}.${memberName(m, sf)}`, "method", m, true);
      }
      const d = add(key, "class", st, true);
      d.skip = skip;
      if (st.name) local.set(name, key);
      if (isExported(st)) exports.push({ name: isDefault(st) ? "default" : name, key });
      continue;
    }
    if (ts.isVariableStatement(st)) {
      const one = st.declarationList.declarations.length === 1;
      // Each declaration of several is a unit of its own, without the statement's keyword.
      if (!one) gap("a statement of several declarations, whose keyword no digest holds");
      for (const d of st.declarationList.declarations) {
        const names = bindingNames(d.name);
        if (names.length === 0) {
          gap("a declaration that binds no name (`const {} = ...`), which no unit holds");
          continue;
        }
        const fn = ts.isIdentifier(d.name) && isFunctionValue(d.initializer);
        const key = `${fn ? "fn" : "const"}:${names.join(",")}`;
        const exec = fn || (d.initializer !== undefined && containsFunction(d.initializer));
        const draft = add(key, fn ? "fn" : "const", one ? st : d, exec);
        if (!one) draft.nodes[draft.nodes.length - 1] = d;
        for (const n of names) local.set(n, key);
        if (isExported(st)) for (const n of names) exports.push({ name: n, key });
      }
      continue;
    }
    if (ts.isEnumDeclaration(st) || ts.isModuleDeclaration(st)) {
      const name = st.name.text;
      const key = `const:${name}`;
      add(key, "const", st, true);
      local.set(name, key);
      if (isExported(st)) exports.push({ name, key });
      continue;
    }
    if (ts.isExportAssignment(st) && !st.isExportEquals) {
      const e = strip(st.expression);
      if (ts.isIdentifier(e)) {
        exports.push({ name: "default", key: `@${e.text}` });
        init(st);
      } else if (ts.isClassExpression(e)) {
        add("class:default", "class", st, true);
        exports.push({ name: "default", key: "class:default" });
      } else {
        const fn = isFunctionValue(e);
        const key = fn ? "fn:default" : "const:default";
        add(key, fn ? "fn" : "const", st, fn || containsFunction(e));
        exports.push({ name: "default", key });
      }
      continue;
    }
    init(st);
  }

  for (const e of exports) {
    if (!e.key?.startsWith("@")) continue;
    const name = e.key.slice(1);
    const imp = importLocals.get(name);
    if (local.has(name)) e.key = local.get(name);
    else if (imp) {
      e.key = undefined;
      e.from = imp.specifier;
      e.imported = imp.imported;
    } else e.key = undefined;
  }

  const units: FileUnit[] = [];
  const known = new Set([...local.keys(), ...importLocals.keys(), ...typeLocals]);
  for (const [key, d] of drafts) {
    const out: string[] = [d.kind];
    const refs = new Set<string>();
    const onIdentifier = (id: ts.Identifier) => {
      const t = id.text;
      if (!known.has(t)) return;
      const imp = importLocals.get(t);
      const p = id.parent;
      if (imp?.imported === "*" && ts.isPropertyAccessExpression(p) && p.expression === id)
        refs.add(`${t}.${p.name.text}`);
      else refs.add(t);
    };
    const scan = (n: ts.Node): void => {
      // A build that emits decorator metadata turns types into code, and the digests leave types out.
      if (ts.isDecorator(n)) gap("a decorator, whose metadata a build may emit from types");
      // The build drops an abstract property and emits a plain one as a field; the digests leave both
      // modifiers out.
      if (
        ts.isPropertyDeclaration(n) &&
        ts.getModifiers(n)?.some((m) => m.kind === K.AbstractKeyword)
      )
        gap("an abstract property, which no digest tells from a field");
      if (ts.isCallExpression(n)) {
        const a = n.arguments[0];
        const spec =
          a && (ts.isStringLiteral(a) || ts.isNoSubstitutionTemplateLiteral(a))
            ? a.text
            : undefined;
        if (spec !== undefined && n.expression.kind === K.ImportKeyword)
          imports.push({ specifier: spec, kind: "dynamic", names: [], in: key });
        else if (
          spec !== undefined &&
          ts.isIdentifier(n.expression) &&
          n.expression.text === "require"
        )
          imports.push({ specifier: spec, kind: "require", names: [], in: key });
      }
      if (
        ts.isNewExpression(n) &&
        ts.isIdentifier(n.expression) &&
        (n.expression.text === "Worker" || n.expression.text === "SharedWorker")
      ) {
        const spec = workerSpec(n.arguments?.[0]);
        if (spec !== undefined)
          imports.push({ specifier: spec, kind: "worker", names: [], in: key });
      }
      ts.forEachChild(n, scan);
    };
    const typeRefs = new Set<string>();
    const types = (n: ts.Node, inType: boolean): void => {
      if (d.skip?.has(n)) return;
      if (inType && ts.isIdentifier(n) && known.has(n.text)) typeRefs.add(n.text);
      ts.forEachChild(n, (c) => types(c, inType || isTypeOnly(c)));
    };
    for (const node of d.nodes) {
      canon(node, out, {
        keepTypes: d.kind === "type",
        onIdentifier,
        skip: d.skip,
        onComment: (pos) => told.add(pos),
      });
      scan(node);
      if (d.kind !== "type") types(node, false);
    }
    // What an import binding means is part of the code that reads it.
    for (const r of [...refs].sort()) {
      const imp = importLocals.get(r.split(".")[0] ?? r);
      if (imp) out.push(`[${r}=${imp.specifier}:${imp.imported}]`);
    }
    if (key === "init") {
      for (const i of imports)
        if (i.kind === "static" || i.kind === "side-effect" || i.kind === "re-export")
          out.push(`[load ${i.specifier}]`);
      for (const p of pragmas) out.push(`[pragma ${p}]`);
    }
    units.push({
      key,
      kind: d.kind,
      executable: d.executable,
      digest: hash(out),
      ranges: d.ranges,
      refs: [...refs].sort(),
      ...(typeRefs.size ? { typeRefs: [...typeRefs].sort() } : {}),
    });
  }
  // A module whose imports load other modules runs something on import even with no statements.
  const loads = (i: FileImport) =>
    i.kind === "static" || i.kind === "side-effect" || i.kind === "re-export";
  if (!drafts.has("init") && (imports.some(loads) || pragmas.length > 0)) {
    const out = ["init"];
    for (const i of imports) if (loads(i)) out.push(`[load ${i.specifier}]`);
    for (const p of pragmas) out.push(`[pragma ${p}]`);
    units.push({
      key: "init",
      kind: "init",
      executable: true,
      digest: hash(out),
      ranges: [],
      refs: [],
    });
  }
  // A binding no unit reads is in no digest, and renaming what it imports may change what loads.
  const read = new Set<string>();
  for (const u of units)
    if (u.kind !== "type") for (const r of u.refs) read.add(r.split(".")[0] ?? r);
  for (const name of importLocals.keys()) {
    if (read.has(name)) continue;
    // One read only in types is erased with its import, unless the build keeps imports as written
    // (verbatimModuleSyntax).
    const inTypes = units.some(
      (u) => u.typeRefs?.includes(name) || (u.kind === "type" && u.refs.includes(name)),
    );
    gap(
      inTypes
        ? `an import binding read only in types, imported without \`type\` (${name})`
        : `an import binding no unit reads (${name})`,
    );
  }
  const errors = (sf as unknown as { parseDiagnostics?: unknown[] }).parseDiagnostics?.length ?? 0;
  const out: FileSummary = {
    units,
    imports,
    exports,
    star,
    locals: Object.fromEntries(local),
    errors,
  };
  const why = incomplete(sf, errors, { gaps, told });
  if (why) out.incomplete = why;
  return out;
}
