// src/canon.ts
import ts from "typescript";
var K = ts.SyntaxKind;
var typeOnlyModifiers = /* @__PURE__ */ new Set([
  K.PublicKeyword,
  K.PrivateKeyword,
  K.ProtectedKeyword,
  K.ReadonlyKeyword,
  K.AbstractKeyword,
  K.OverrideKeyword,
  K.DeclareKeyword,
  K.InKeyword,
  K.OutKeyword
]);
function hasModifier(n, kind) {
  return ts.canHaveModifiers(n) && (ts.getModifiers(n)?.some((m) => m.kind === kind) ?? false);
}
function isTypeOnly(n) {
  switch (n.kind) {
    case K.InterfaceDeclaration:
    case K.TypeAliasDeclaration:
    case K.TypeParameter:
    case K.IndexSignature:
    case K.NamespaceExportDeclaration:
      return true;
    case K.ImportDeclaration: {
      const c = n.importClause;
      return c?.phaseModifier === K.TypeKeyword;
    }
    case K.ImportEqualsDeclaration:
      return n.isTypeOnly;
    case K.ExportDeclaration:
      return n.isTypeOnly;
    case K.ImportSpecifier:
    case K.ExportSpecifier:
      return n.isTypeOnly;
    case K.FunctionDeclaration:
    case K.MethodDeclaration:
    case K.Constructor:
    case K.GetAccessor:
    case K.SetAccessor:
      return n.body === void 0;
    case K.Parameter: {
      const name = n.name;
      return ts.isIdentifier(name) && name.text === "this";
    }
    case K.HeritageClause:
      return n.token === K.ImplementsKeyword;
    case K.ExpressionWithTypeArguments:
      return false;
  }
  if (hasModifier(n, K.DeclareKeyword)) return true;
  if (ts.isTypeNode(n)) return true;
  return false;
}
function jsxText(text) {
  const lines = text.split(/\r\n|\n|\r/);
  let lastNonEmpty = 0;
  for (let i = 0; i < lines.length; i++) if (/[^ \t]/.test(lines[i] ?? "")) lastNonEmpty = i;
  let out = "";
  for (let i = 0; i < lines.length; i++) {
    let line = (lines[i] ?? "").replace(/\t/g, " ");
    if (i !== 0) line = line.replace(/^ +/, "");
    if (i !== lines.length - 1) line = line.replace(/ +$/, "");
    if (line) {
      if (i !== lastNonEmpty) line += " ";
      out += line;
    }
  }
  return out;
}
// @__NO_SIDE_EFFECTS__
function buildComment(text) {
  const found = [];
  if (/[#@]__PURE__/.test(text)) found.push("__PURE__");
  if (/[#@]__NO_SIDE_EFFECTS__/.test(text)) found.push("__NO_SIDE_EFFECTS__");
  if (/@vite-ignore\b/.test(text)) found.push("@vite-ignore");
  for (const m of text.matchAll(/\bwebpack[A-Z][A-Za-z]*\s*:\s*(?:"[^"]*"|'[^']*'|[^\s,*]+)/g))
    found.push(m[0].replace(/\s+/g, ""));
  return found.length ? found.join(" ") : void 0;
}
function jsxPragmas(text) {
  const out = [];
  for (const r of ts.getLeadingCommentRanges(text, 0) ?? []) {
    const c = text.slice(r.pos, r.end);
    for (const m of c.matchAll(/@(jsx|jsxFrag|jsxImportSource|jsxRuntime)\s+([^\s*]+)/g))
      out.push(`@${m[1]} ${m[2]}`);
  }
  return out;
}
var typeWrappers = /* @__PURE__ */ new Set([
  K.AsExpression,
  K.SatisfiesExpression,
  K.NonNullExpression,
  K.TypeAssertionExpression,
  K.ExpressionWithTypeArguments
]);
function isReadPosition(id) {
  const p = id.parent;
  if (!p) return false;
  if (ts.isPropertyAccessExpression(p) && p.name === id) return false;
  if (ts.isQualifiedName(p) && p.right === id) return false;
  if ((ts.isPropertyAssignment(p) || ts.isMethodDeclaration(p) || ts.isPropertyDeclaration(p) || ts.isGetAccessorDeclaration(p) || ts.isSetAccessorDeclaration(p) || ts.isEnumMember(p) || ts.isPropertySignature(p) || ts.isMethodSignature(p)) && p.name === id)
    return false;
  if (ts.isBindingElement(p) && p.propertyName === id) return false;
  if ((ts.isVariableDeclaration(p) || ts.isFunctionDeclaration(p) || ts.isFunctionExpression(p) || ts.isClassDeclaration(p) || ts.isClassExpression(p) || ts.isParameter(p) || ts.isBindingElement(p) || ts.isLabeledStatement(p) || ts.isBreakOrContinueStatement(p) || ts.isImportSpecifier(p) || ts.isImportClause(p) || ts.isNamespaceImport(p) || ts.isEnumDeclaration(p) || ts.isModuleDeclaration(p)) && p.name === id)
    return false;
  if (ts.isJsxAttribute(p) && p.name === id) return false;
  if (ts.isMetaProperty(p)) return false;
  if (ts.isExportSpecifier(p) && p.name === id && p.propertyName !== void 0) return false;
  return true;
}
function canon(node, out, opt = {}) {
  const text = node.getSourceFile()?.text;
  const told = /* @__PURE__ */ new Set();
  const comments = (n) => {
    if (!text || n.kind === K.SourceFile) return;
    const start = n.getStart();
    const ranges = [
      // one that ends the line before belongs to what came before
      ...(ts.getTrailingCommentRanges(text, n.pos) ?? []).filter(
        (r) => !text.slice(r.end, start).includes("\n")
      ),
      ...ts.getLeadingCommentRanges(text, n.pos) ?? []
    ];
    for (const r of ranges) {
      if (r.end > start) continue;
      if (told.has(r.pos)) continue;
      told.add(r.pos);
      const c = /* @__PURE__ */ buildComment(text.slice(r.pos, r.end));
      if (c) {
        out.push(` [c ${c}]`);
        opt.onComment?.(r.pos);
      }
    }
  };
  const visit = (n) => {
    if (opt.skip?.has(n)) return;
    if (!opt.keepTypes) {
      if (typeWrappers.has(n.kind)) {
        visit(n.expression);
        return;
      }
      if (isTypeOnly(n)) return;
      if (ts.isModifier(n) && typeOnlyModifiers.has(n.kind) && !ts.isParameter(n.parent)) return;
      if (n.kind === K.ExclamationToken || n.kind === K.QuestionToken) return;
    }
    if (n.kind === K.ParenthesizedExpression) {
      visit(n.expression);
      return;
    }
    if (n.kind === K.EmptyStatement && n.parent && isStatementList(n.parent)) return;
    if (n.kind === K.SemicolonClassElement) return;
    let head = `(${n.kind}`;
    if (n.flags & ts.NodeFlags.OptionalChain) head += "?";
    if (ts.isVariableDeclarationList(n)) head += `/${n.flags & ts.NodeFlags.BlockScoped}`;
    if (ts.isVariableDeclarationList(n) && n.flags & ts.NodeFlags.Using) head += "u";
    out.push(head);
    comments(n);
    switch (n.kind) {
      case K.Identifier:
        out.push(` ${n.text}`);
        if (opt.onIdentifier && isReadPosition(n))
          opt.onIdentifier(n);
        break;
      case K.PrivateIdentifier:
        out.push(` ${n.text}`);
        break;
      case K.StringLiteral:
      case K.NumericLiteral:
      case K.BigIntLiteral:
        out.push(` ${JSON.stringify(n.text)}`);
        break;
      case K.RegularExpressionLiteral:
        out.push(` ${JSON.stringify(n.text)}`);
        break;
      case K.NoSubstitutionTemplateLiteral:
      case K.TemplateHead:
      case K.TemplateMiddle:
      case K.TemplateTail: {
        const t = n;
        out.push(` ${JSON.stringify(t.rawText ?? t.text)}`);
        break;
      }
      case K.JsxText: {
        const t = jsxText(n.text);
        out.push(` ${JSON.stringify(t)}`);
        break;
      }
      case K.HeritageClause: {
        const h = n;
        out.push(` ${h.token}`);
        for (const t of h.types) visit(opt.keepTypes ? t : t.expression);
        out.push(")");
        return;
      }
    }
    if (n.kind === K.JsxText && (out[out.length - 1] ?? "") === ' ""') {
      out.pop();
      out.pop();
      return;
    }
    ts.forEachChild(n, visit, (nodes) => {
      for (const c of nodes) visit(c);
    });
    out.push(")");
  };
  visit(node);
}
function isStatementList(n) {
  return ts.isBlock(n) || ts.isSourceFile(n) || ts.isModuleBlock(n) || ts.isCaseClause(n) || ts.isDefaultClause(n);
}

// src/units.ts
import { createHash } from "node:crypto";
import ts3 from "typescript";

// src/complete.ts
import ts2 from "typescript";
var docTags = /* @__PURE__ */ new Set([
  "abstract",
  "access",
  "alias",
  "alpha",
  "async",
  "augments",
  "author",
  "beta",
  "borrows",
  "callback",
  "class",
  "classdesc",
  "constant",
  "constructor",
  "const",
  "copyright",
  "default",
  "defaultvalue",
  "deprecated",
  "description",
  "desc",
  "enum",
  "event",
  "eventproperty",
  "example",
  "experimental",
  "exports",
  "extends",
  "external",
  "file",
  "fileoverview",
  "fires",
  "function",
  "func",
  "generator",
  "global",
  "hideconstructor",
  "ignore",
  "implements",
  "inheritdoc",
  "inner",
  "instance",
  "interface",
  "internal",
  "kind",
  "label",
  "lends",
  "link",
  "linkcode",
  "linkplain",
  "listens",
  "member",
  "memberof",
  "method",
  "mixes",
  "mixin",
  "module",
  "name",
  "namespace",
  "override",
  "overload",
  "package",
  "packagedocumentation",
  "param",
  "private",
  "privateremarks",
  "prop",
  "property",
  "protected",
  "public",
  "readonly",
  "remarks",
  "requires",
  "return",
  "returns",
  "satisfies",
  "sealed",
  "see",
  "since",
  "static",
  "summary",
  "template",
  "this",
  "throws",
  "exception",
  "todo",
  "tutorial",
  "type",
  "typedef",
  "typeparam",
  "variation",
  "version",
  "virtual",
  "yields",
  "yield",
  "ts-check",
  "ts-nocheck",
  "ts-ignore",
  "ts-expect-error"
]);
var jsxTags = /* @__PURE__ */ new Set(["jsx", "jsxfrag", "jsximportsource", "jsxruntime"]);
var toolText = [
  [/^\/[/*]!/, "a legal comment, which the bundle keeps"],
  [/[@#]__[A-Z_]+__/, "an annotation a bundler reads"],
  [/^\/\/\/\s*<(?:reference|amd-)/, "a triple-slash directive"],
  [/[#@]\s*source(?:Mapping)?URL\s*=/, "a source map reference"],
  [/\b(?:c8|v8|istanbul)\s+ignore\b/, "a coverage directive"],
  [/\bnode:coverage\b/, "a coverage directive"],
  [/\beslint-env\b/, "an environment a tool reads"]
];
var tagPattern = /(?:^|[\s*{(])@([A-Za-z][\w-]*)(?![\w/-])/g;
function toolComment(comment, told = false, head = false) {
  const build = buildComment(comment);
  let rest = comment;
  if (build !== void 0) {
    if (!told) return "a comment a bundler reads, outside every digest";
    rest = rest.replace(/[#@]__(?:PURE|NO_SIDE_EFFECTS)__/g, "").replace(/@vite-ignore\b/g, "").replace(/\bwebpack[A-Z][A-Za-z]*\s*:/g, "");
  }
  for (const [re, why] of toolText) if (re.test(rest)) return why;
  for (const m of rest.matchAll(tagPattern)) {
    const tag = (m[1] ?? "").toLowerCase();
    if (docTags.has(tag)) continue;
    if (jsxTags.has(tag)) {
      if (head) continue;
      return `a JSX pragma (@${m[1]}) after the first token`;
    }
    if (tag === "license" || tag === "preserve") return "a legal comment, which the bundle keeps";
    return `a pragma (@${m[1]})`;
  }
  return void 0;
}
function commentsOf(sf) {
  const text = sf.text;
  const seen = /* @__PURE__ */ new Set();
  const out = [];
  const take = (ranges) => {
    for (const r of ranges ?? []) {
      if (seen.has(r.pos)) continue;
      seen.add(r.pos);
      out.push({ pos: r.pos, text: text.slice(r.pos, r.end) });
    }
  };
  const visit = (n) => {
    take(ts2.getLeadingCommentRanges(text, n.pos));
    take(ts2.getTrailingCommentRanges(text, n.pos));
    take(ts2.getTrailingCommentRanges(text, n.end));
    for (const c of n.getChildren(sf)) visit(c);
  };
  visit(sf);
  return out.sort((a, b) => a.pos - b.pos);
}
function incomplete(sf, errors, cov) {
  if (errors > 0) return `${errors} syntax error${errors === 1 ? "" : "s"}`;
  if (sf.text.startsWith("\uFEFF")) return "a byte order mark";
  if (sf.text.startsWith("#!")) return "a shebang line";
  if (cov.gaps.length > 0) return cov.gaps[0];
  const head = new Set((ts2.getLeadingCommentRanges(sf.text, 0) ?? []).map((r) => r.pos));
  for (const c of commentsOf(sf)) {
    const why = toolComment(c.text, cov.told.has(c.pos), head.has(c.pos));
    if (why) return why;
  }
  return void 0;
}

// src/units.ts
var K2 = ts3.SyntaxKind;
function scriptKind(path) {
  if (path.endsWith(".tsx")) return ts3.ScriptKind.TSX;
  if (/\.[mc]?ts$/.test(path)) return ts3.ScriptKind.TS;
  if (path.endsWith(".jsx")) return ts3.ScriptKind.JSX;
  return ts3.ScriptKind.JS;
}
function parse(path, text) {
  return ts3.createSourceFile(path, text, ts3.ScriptTarget.Latest, true, scriptKind(path));
}
function hash(parts) {
  const h = createHash("sha256");
  for (const p of parts) h.update(p);
  return h.digest("hex").slice(0, 32);
}
function strip(e) {
  while (ts3.isParenthesizedExpression(e) || ts3.isAsExpression(e) || ts3.isSatisfiesExpression(e) || ts3.isNonNullExpression(e) || ts3.isTypeAssertionExpression(e))
    e = e.expression;
  return e;
}
function isFunctionValue(e, depth = 0) {
  if (!e) return false;
  e = strip(e);
  if (ts3.isArrowFunction(e) || ts3.isFunctionExpression(e)) return true;
  if (ts3.isCallExpression(e) && depth < 3)
    return e.arguments.some((a) => isFunctionValue(a, depth + 1));
  return false;
}
function containsFunction(n) {
  let found = false;
  const visit = (c) => {
    if (found) return;
    if (ts3.isFunctionLike(c) && !ts3.isTypeNode(c) && "body" in c && c.body) {
      found = true;
      return;
    }
    if (ts3.isClassLike(c)) {
      found = true;
      return;
    }
    ts3.forEachChild(c, visit);
  };
  visit(n);
  return found;
}
function bindingNames(name, out = []) {
  if (ts3.isIdentifier(name)) out.push(name.text);
  else for (const el of name.elements) if (ts3.isBindingElement(el)) bindingNames(el.name, out);
  return out;
}
function memberName(m, sf) {
  const n = m.name;
  let base;
  if (!n) base = ts3.isConstructorDeclaration(m) ? "constructor" : "static{}";
  else if (ts3.isIdentifier(n) || ts3.isPrivateIdentifier(n)) base = n.text;
  else if (ts3.isStringLiteral(n) || ts3.isNumericLiteral(n)) base = n.text;
  else base = n.getText(sf).replace(/\s+/g, "");
  if (ts3.isGetAccessorDeclaration(m)) base = `get ${base}`;
  if (ts3.isSetAccessorDeclaration(m)) base = `set ${base}`;
  const isStatic = ts3.canHaveModifiers(m) && ts3.getModifiers(m)?.some((x) => x.kind === K2.StaticKeyword);
  return isStatic ? `static ${base}` : base;
}
function isMethodLike(m) {
  if (ts3.isMethodDeclaration(m) || ts3.isGetAccessorDeclaration(m) || ts3.isSetAccessorDeclaration(m))
    return m.body !== void 0;
  if (ts3.isConstructorDeclaration(m)) return m.body !== void 0;
  if (ts3.isPropertyDeclaration(m)) return isFunctionValue(m.initializer);
  return false;
}
function workerSpec(a) {
  if (!a) return void 0;
  if (ts3.isStringLiteral(a) || ts3.isNoSubstitutionTemplateLiteral(a)) return a.text;
  if (ts3.isNewExpression(a) && ts3.isIdentifier(a.expression) && a.expression.text === "URL") {
    const [first, second] = a.arguments ?? [];
    if (first && (ts3.isStringLiteral(first) || ts3.isNoSubstitutionTemplateLiteral(first)) && second && ts3.isPropertyAccessExpression(second) && ts3.isMetaProperty(second.expression))
      return first.text;
  }
  return void 0;
}
function hasDeclare(n) {
  return ts3.canHaveModifiers(n) && (ts3.getModifiers(n)?.some((m) => m.kind === K2.DeclareKeyword) ?? false);
}
function summarize(path, text) {
  const sf = parse(path, text);
  const pragmas = jsxPragmas(text);
  const drafts = /* @__PURE__ */ new Map();
  const local = /* @__PURE__ */ new Map();
  const imports = [];
  const exports = [];
  const star = [];
  const importLocals = /* @__PURE__ */ new Map();
  const typeLocals = /* @__PURE__ */ new Set();
  const gaps = [];
  const told = /* @__PURE__ */ new Set();
  const gap = (why2) => {
    if (!gaps.includes(why2)) gaps.push(why2);
  };
  const range = (n) => [n.getStart(sf), n.end];
  const add = (key2, kind, node, executable, r = range(node)) => {
    let d = drafts.get(key2);
    if (!d) {
      d = { kind, nodes: [], ranges: [], executable: false };
      drafts.set(key2, d);
    }
    d.nodes.push(node);
    d.ranges.push(r);
    d.executable ||= executable;
    return d;
  };
  const init = (node) => add("init", "init", node, true);
  const isExported = (n) => ts3.canHaveModifiers(n) && (ts3.getModifiers(n)?.some((m) => m.kind === K2.ExportKeyword) ?? false);
  const isDefault = (n) => ts3.canHaveModifiers(n) && (ts3.getModifiers(n)?.some((m) => m.kind === K2.DefaultKeyword) ?? false);
  for (const st of sf.statements) {
    if (ts3.isImportDeclaration(st)) {
      const spec = st.moduleSpecifier.text;
      const c = st.importClause;
      const typeNames = [];
      if (c && isTypeOnly(st)) {
        if (c.name) typeNames.push({ local: c.name.text, imported: "default" });
        const tb = c.namedBindings;
        if (tb && ts3.isNamespaceImport(tb)) typeNames.push({ local: tb.name.text, imported: "*" });
        if (tb && ts3.isNamedImports(tb))
          for (const e of tb.elements)
            typeNames.push({ local: e.name.text, imported: (e.propertyName ?? e.name).text });
      } else if (c?.namedBindings && ts3.isNamedImports(c.namedBindings)) {
        for (const e of c.namedBindings.elements)
          if (e.isTypeOnly)
            typeNames.push({ local: e.name.text, imported: (e.propertyName ?? e.name).text });
      }
      if (typeNames.length) {
        imports.push({ specifier: spec, kind: "type", names: typeNames });
        for (const n of typeNames) typeLocals.add(n.local);
      }
      if (isTypeOnly(st)) continue;
      if (st.attributes || c?.phaseModifier !== void 0 && c.phaseModifier !== K2.TypeKeyword)
        gap("an import's attributes or phase, which no digest holds");
      if (!c) {
        imports.push({ specifier: spec, kind: "side-effect", names: [] });
        init(st);
        continue;
      }
      const names = [];
      if (c.name) names.push({ local: c.name.text, imported: "default" });
      const b = c.namedBindings;
      if (b && ts3.isNamespaceImport(b)) names.push({ local: b.name.text, imported: "*" });
      if (b && ts3.isNamedImports(b)) {
        for (const e of b.elements)
          if (!e.isTypeOnly)
            names.push({ local: e.name.text, imported: (e.propertyName ?? e.name).text });
      }
      for (const n of names) importLocals.set(n.local, { specifier: spec, imported: n.imported });
      imports.push({ specifier: spec, kind: "static", names });
      continue;
    }
    if (ts3.isExportDeclaration(st)) {
      if (isTypeOnly(st)) {
        const spec2 = st.moduleSpecifier && st.moduleSpecifier.text;
        const c2 = st.exportClause;
        if (spec2 !== void 0 && c2 && ts3.isNamedExports(c2))
          for (const e of c2.elements)
            exports.push({
              name: e.name.text,
              from: spec2,
              imported: (e.propertyName ?? e.name).text
            });
        continue;
      }
      const spec = st.moduleSpecifier && st.moduleSpecifier.text;
      const c = st.exportClause;
      if (spec !== void 0) {
        const names = [];
        if (!c) star.push(spec);
        else if (ts3.isNamespaceExport(c)) {
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
      } else if (c && ts3.isNamedExports(c)) {
        for (const e of c.elements)
          if (!e.isTypeOnly)
            exports.push({ name: e.name.text, key: `@${(e.propertyName ?? e.name).text}` });
      }
      init(st);
      continue;
    }
    if (ts3.isFunctionDeclaration(st) && !st.body && !hasDeclare(st)) continue;
    if (isTypeOnly(st)) {
      const name = st.name;
      if (name && (ts3.isIdentifier(name) || ts3.isStringLiteral(name))) {
        const key2 = `type:${name.text}`;
        add(key2, "type", st, false);
        if (!local.has(name.text)) local.set(name.text, key2);
        if (isExported(st)) exports.push({ name: name.text, key: key2 });
      }
      continue;
    }
    if (ts3.isFunctionDeclaration(st)) {
      const name = st.name?.text ?? "default";
      const key2 = `fn:${name}`;
      add(key2, "fn", st, true);
      if (st.name) local.set(name, key2);
      if (isExported(st)) exports.push({ name: isDefault(st) ? "default" : name, key: key2 });
      continue;
    }
    if (ts3.isClassDeclaration(st)) {
      const name = st.name?.text ?? "default";
      const key2 = `class:${name}`;
      const skip = /* @__PURE__ */ new Set();
      for (const m of st.members) {
        if (!isMethodLike(m) || isTypeOnly(m)) continue;
        skip.add(m);
        add(`method:${name}.${memberName(m, sf)}`, "method", m, true);
      }
      const d = add(key2, "class", st, true);
      d.skip = skip;
      if (st.name) local.set(name, key2);
      if (isExported(st)) exports.push({ name: isDefault(st) ? "default" : name, key: key2 });
      continue;
    }
    if (ts3.isVariableStatement(st)) {
      const one = st.declarationList.declarations.length === 1;
      if (!one) gap("a statement of several declarations, whose keyword no digest holds");
      for (const d of st.declarationList.declarations) {
        const names = bindingNames(d.name);
        if (names.length === 0) {
          gap("a declaration that binds no name (`const {} = ...`), which no unit holds");
          continue;
        }
        const fn = ts3.isIdentifier(d.name) && isFunctionValue(d.initializer);
        const key2 = `${fn ? "fn" : "const"}:${names.join(",")}`;
        const exec = fn || d.initializer !== void 0 && containsFunction(d.initializer);
        const draft = add(key2, fn ? "fn" : "const", one ? st : d, exec);
        if (!one) draft.nodes[draft.nodes.length - 1] = d;
        for (const n of names) local.set(n, key2);
        if (isExported(st)) for (const n of names) exports.push({ name: n, key: key2 });
      }
      continue;
    }
    if (ts3.isEnumDeclaration(st) || ts3.isModuleDeclaration(st)) {
      const name = st.name.text;
      const key2 = `const:${name}`;
      add(key2, "const", st, true);
      local.set(name, key2);
      if (isExported(st)) exports.push({ name, key: key2 });
      continue;
    }
    if (ts3.isExportAssignment(st) && !st.isExportEquals) {
      const e = strip(st.expression);
      if (ts3.isIdentifier(e)) {
        exports.push({ name: "default", key: `@${e.text}` });
        init(st);
      } else if (ts3.isClassExpression(e)) {
        add("class:default", "class", st, true);
        exports.push({ name: "default", key: "class:default" });
      } else {
        const fn = isFunctionValue(e);
        const key2 = fn ? "fn:default" : "const:default";
        add(key2, fn ? "fn" : "const", st, fn || containsFunction(e));
        exports.push({ name: "default", key: key2 });
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
      e.key = void 0;
      e.from = imp.specifier;
      e.imported = imp.imported;
    } else e.key = void 0;
  }
  const units = [];
  const known = /* @__PURE__ */ new Set([...local.keys(), ...importLocals.keys(), ...typeLocals]);
  for (const [key2, d] of drafts) {
    const out2 = [d.kind];
    const refs = /* @__PURE__ */ new Set();
    const onIdentifier = (id) => {
      const t = id.text;
      if (!known.has(t)) return;
      const imp = importLocals.get(t);
      const p = id.parent;
      if (imp?.imported === "*" && ts3.isPropertyAccessExpression(p) && p.expression === id)
        refs.add(`${t}.${p.name.text}`);
      else refs.add(t);
    };
    const scan = (n) => {
      if (ts3.isDecorator(n)) gap("a decorator, whose metadata a build may emit from types");
      if (ts3.isPropertyDeclaration(n) && ts3.getModifiers(n)?.some((m) => m.kind === K2.AbstractKeyword))
        gap("an abstract property, which no digest tells from a field");
      if (ts3.isCallExpression(n)) {
        const a = n.arguments[0];
        const spec = a && (ts3.isStringLiteral(a) || ts3.isNoSubstitutionTemplateLiteral(a)) ? a.text : void 0;
        if (spec !== void 0 && n.expression.kind === K2.ImportKeyword)
          imports.push({ specifier: spec, kind: "dynamic", names: [], in: key2 });
        else if (spec !== void 0 && ts3.isIdentifier(n.expression) && n.expression.text === "require")
          imports.push({ specifier: spec, kind: "require", names: [], in: key2 });
      }
      if (ts3.isNewExpression(n) && ts3.isIdentifier(n.expression) && (n.expression.text === "Worker" || n.expression.text === "SharedWorker")) {
        const spec = workerSpec(n.arguments?.[0]);
        if (spec !== void 0)
          imports.push({ specifier: spec, kind: "worker", names: [], in: key2 });
      }
      ts3.forEachChild(n, scan);
    };
    const typeRefs = /* @__PURE__ */ new Set();
    const types = (n, inType) => {
      if (d.skip?.has(n)) return;
      if (inType && ts3.isIdentifier(n) && known.has(n.text)) typeRefs.add(n.text);
      ts3.forEachChild(n, (c) => types(c, inType || isTypeOnly(c)));
    };
    for (const node of d.nodes) {
      canon(node, out2, {
        keepTypes: d.kind === "type",
        onIdentifier,
        skip: d.skip,
        onComment: (pos) => told.add(pos)
      });
      scan(node);
      if (d.kind !== "type") types(node, false);
    }
    for (const r of [...refs].sort()) {
      const imp = importLocals.get(r.split(".")[0] ?? r);
      if (imp) out2.push(`[${r}=${imp.specifier}:${imp.imported}]`);
    }
    if (key2 === "init") {
      for (const i of imports)
        if (i.kind === "static" || i.kind === "side-effect" || i.kind === "re-export")
          out2.push(`[load ${i.specifier}]`);
      for (const p of pragmas) out2.push(`[pragma ${p}]`);
    }
    units.push({
      key: key2,
      kind: d.kind,
      executable: d.executable,
      digest: hash(out2),
      ranges: d.ranges,
      refs: [...refs].sort(),
      ...typeRefs.size ? { typeRefs: [...typeRefs].sort() } : {}
    });
  }
  const loads = (i) => i.kind === "static" || i.kind === "side-effect" || i.kind === "re-export";
  if (!drafts.has("init") && (imports.some(loads) || pragmas.length > 0)) {
    const out2 = ["init"];
    for (const i of imports) if (loads(i)) out2.push(`[load ${i.specifier}]`);
    for (const p of pragmas) out2.push(`[pragma ${p}]`);
    units.push({
      key: "init",
      kind: "init",
      executable: true,
      digest: hash(out2),
      ranges: [],
      refs: []
    });
  }
  const read = /* @__PURE__ */ new Set();
  for (const u of units)
    if (u.kind !== "type") for (const r of u.refs) read.add(r.split(".")[0] ?? r);
  for (const name of importLocals.keys()) {
    if (read.has(name)) continue;
    const inTypes = units.some(
      (u) => u.typeRefs?.includes(name) || u.kind === "type" && u.refs.includes(name)
    );
    gap(
      inTypes ? `an import binding read only in types, imported without \`type\` (${name})` : `an import binding no unit reads (${name})`
    );
  }
  const errors = sf.parseDiagnostics?.length ?? 0;
  const out = {
    units,
    imports,
    exports,
    star,
    locals: Object.fromEntries(local),
    errors
  };
  const why = incomplete(sf, errors, { gaps, told });
  if (why) out.incomplete = why;
  return out;
}

// src/css.ts
function normalizeCSS(text) {
  let out = "";
  let i = 0;
  let space = false;
  const n = text.length;
  while (i < n) {
    const c = text[i];
    if (c === "/" && text[i + 1] === "*") {
      const end = text.indexOf("*/", i + 2);
      i = end < 0 ? n : end + 2;
      space = true;
      continue;
    }
    if (c === '"' || c === "'") {
      let j = i + 1;
      while (j < n && text[j] !== c) j += text[j] === "\\" ? 2 : 1;
      if (space && out && !/[{};,>:(]$/.test(out)) out += " ";
      space = false;
      out += text.slice(i, j + 1);
      i = j + 1;
      continue;
    }
    if (/\s/.test(c)) {
      space = true;
      i++;
      continue;
    }
    if (space && out && !/[{};,>:(]$/.test(out) && !/[{};,>)]/.test(c)) out += " ";
    space = false;
    if (c === "}" && out.endsWith(";")) out = out.slice(0, -1);
    out += c;
    i++;
  }
  return out;
}
function cssDigest(text) {
  return hash(["css", normalizeCSS(text)]);
}

// src/ids.ts
var scriptNamespace = "ts";
var styleNamespace = "css";
function scriptId(file, key2) {
  return `${scriptNamespace}:${file}#${key2}`;
}
function styleId(file) {
  return `${styleNamespace}:${file}`;
}
function parseId(id) {
  if (id.startsWith(`${styleNamespace}:`)) return { file: id.slice(styleNamespace.length + 1) };
  if (!id.startsWith(`${scriptNamespace}:`)) return void 0;
  const rest = id.slice(scriptNamespace.length + 1);
  const at = rest.indexOf("#");
  if (at < 0) return void 0;
  return { file: rest.slice(0, at), key: rest.slice(at + 1) };
}
var scriptExtensions = /\.(?:[mc]?[jt]s|[jt]sx)$/;
function isScript(path) {
  return scriptExtensions.test(path) && !/\.d\.[mc]?ts$/.test(path);
}
function isStyle(path) {
  return path.endsWith(".css");
}
var maxLabel = 80;
function shortPath(file) {
  const parts = file.split("/");
  const src = parts.lastIndexOf("src");
  if (src >= 0 && src < parts.length - 1) return parts.slice(src + 1).join("/");
  return parts.slice(-3).join("/");
}
function clip(s) {
  const r = [...s];
  if (r.length <= maxLabel) return s;
  const half = Math.floor((maxLabel - 1) / 2);
  return `${r.slice(0, half).join("")}\u2026${r.slice(r.length - (maxLabel - 1 - half)).join("")}`;
}
function label(id) {
  const p = parseId(id);
  if (!p) return clip(id);
  const where = shortPath(p.file);
  if (p.key === void 0) return clip(`the stylesheet ${where}`);
  if (p.key === "init") return clip(`the module ${where}`);
  const colon = p.key.indexOf(":");
  const kind = colon < 0 ? "" : p.key.slice(0, colon);
  const name = colon < 0 ? p.key.replace(/^@/, "") : p.key.slice(colon + 1);
  const what = kind === "type" ? `type ${name}` : kind === "class" ? `class ${name}` : name;
  return clip(`${what} in ${where}`);
}

// src/detect.ts
var candidates = [
  "**/*.ts",
  "**/*.tsx",
  "**/*.mts",
  "**/*.cts",
  "**/*.js",
  "**/*.jsx",
  "**/*.mjs",
  "**/*.cjs",
  "**/*.css"
];
var generatedHeader = /^(?:\s*(?:\/\/|\/\*+|\*|#)[^\n]*\n){0,5}?\s*(?:\/\/|\/\*+|\*)\s*(?:Code generated(?: by ([^\s.]+))?|@generated|This file (?:was|is) (?:auto(?:matically)?[- ])?generated(?: by ([^\s.]+))?|DO NOT EDIT)/i;
function isBuilt(path, text) {
  const firstLine = text.slice(0, text.indexOf("\n") >>> 0);
  return firstLine.length > 5e3 && /\.(?:m?js|cjs|css)$/.test(path);
}
function detect(path, text) {
  if (path.split("/").includes("node_modules")) return void 0;
  if (!isScript(path) && !isStyle(path)) return void 0;
  if (isBuilt(path, text)) return void 0;
  const g = generatedHeader.exec(text.slice(0, 2e3));
  if (g) {
    const tool = g[1] ?? g[2];
    return tool ? { path, role: "generated", tool } : { path, role: "generated" };
  }
  return { path, role: "source" };
}

// src/tree.ts
import { createHash as createHash2 } from "node:crypto";
import { mkdirSync, readFileSync, renameSync, writeFileSync } from "node:fs";
import { join } from "node:path";
var summaryVersion = "6";
function listFiles(files) {
  return [...new Set(files)].filter((f) => isScript(f) || isStyle(f)).sort();
}
function key(path, text) {
  const ext = path.slice(path.lastIndexOf("."));
  return createHash2("sha256").update(`${summaryVersion}\0${ext}\0${text}`).digest("hex");
}
function summaryOf(path, text, cache) {
  const k = key(path, text);
  const file = cache ? join(cache, "summaries", k.slice(0, 2), `${k}.json`) : "";
  if (cache) {
    try {
      return JSON.parse(readFileSync(file, "utf8"));
    } catch {
    }
  }
  const s = isStyle(path) ? { css: cssDigest(text) } : summarize(path, text);
  if (cache) {
    mkdirSync(join(cache, "summaries", k.slice(0, 2)), { recursive: true });
    const tmp = `${file}.${process.pid}.tmp`;
    writeFileSync(tmp, JSON.stringify(s));
    renameSync(tmp, file);
  }
  return s;
}
function readTree(root, files, cache) {
  const out = /* @__PURE__ */ new Map();
  for (const f of listFiles(files)) {
    let text;
    try {
      text = readFileSync(join(root, f), "utf8");
    } catch {
      continue;
    }
    if (isBuilt(f, text)) continue;
    out.set(f, summaryOf(f, text, cache));
  }
  return out;
}
export {
  candidates,
  canon,
  cssDigest,
  detect,
  hash,
  isBuilt,
  isScript,
  isStyle,
  isTypeOnly,
  jsxText,
  label,
  listFiles,
  normalizeCSS,
  parse,
  parseId,
  readTree,
  scriptId,
  scriptKind,
  scriptNamespace,
  shortPath,
  styleId,
  styleNamespace,
  summarize,
  summaryOf,
  summaryVersion
};
/**
 * JSDoc and TSDoc tags, which document code and change nothing a test runs,
 * and the type checker's own, which only the type check reads. A comment
 * holding any other `@word` is taken for a pragma a tool reads. `@license`
 * and `@preserve` are not here: they make a legal comment, which a bundler
 * keeps in its output.
 */
