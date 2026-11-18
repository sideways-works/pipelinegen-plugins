// The canonical form a digest is taken over: it keeps what can change what the code does at run
// time and drops comments, layout, quote style and, in runtime mode, every type.
import ts from "typescript";

const K = ts.SyntaxKind;

// A constructor parameter keeps these, because there they declare a field.
const typeOnlyModifiers = new Set<ts.SyntaxKind>([
  K.PublicKeyword,
  K.PrivateKeyword,
  K.ProtectedKeyword,
  K.ReadonlyKeyword,
  K.AbstractKeyword,
  K.OverrideKeyword,
  K.DeclareKeyword,
  K.InKeyword,
  K.OutKeyword,
]);

function hasModifier(n: ts.Node, kind: ts.SyntaxKind): boolean {
  return ts.canHaveModifiers(n) && (ts.getModifiers(n)?.some((m) => m.kind === kind) ?? false);
}

export function isTypeOnly(n: ts.Node): boolean {
  switch (n.kind) {
    case K.InterfaceDeclaration:
    case K.TypeAliasDeclaration:
    case K.TypeParameter:
    case K.IndexSignature:
    case K.NamespaceExportDeclaration:
      return true;
    case K.ImportDeclaration: {
      const c = (n as ts.ImportDeclaration).importClause;
      return c?.phaseModifier === K.TypeKeyword;
    }
    case K.ImportEqualsDeclaration:
      return (n as ts.ImportEqualsDeclaration).isTypeOnly;
    case K.ExportDeclaration:
      return (n as ts.ExportDeclaration).isTypeOnly;
    case K.ImportSpecifier:
    case K.ExportSpecifier:
      return (n as ts.ImportSpecifier | ts.ExportSpecifier).isTypeOnly;
    case K.FunctionDeclaration:
    case K.MethodDeclaration:
    case K.Constructor:
    case K.GetAccessor:
    case K.SetAccessor:
      // overload signatures and abstract members have no body
      return (n as ts.FunctionLikeDeclaration).body === undefined;
    case K.Parameter: {
      const name = (n as ts.ParameterDeclaration).name;
      return ts.isIdentifier(name) && name.text === "this";
    }
    case K.HeritageClause:
      return (n as ts.HeritageClause).token === K.ImplementsKeyword;
    case K.ExpressionWithTypeArguments:
      // in an extends clause it is a value; handled by the caller
      return false;
  }
  if (hasModifier(n, K.DeclareKeyword)) return true;
  if (ts.isTypeNode(n)) return true;
  return false;
}

/** React's rule for the text between JSX tags: lines trimmed, blank ones dropped. */
export function jsxText(text: string): string {
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

/**
 * What a bundler reads in a comment beside an expression or a declaration,
 * canonically: the annotations that let a minifier drop a call
 * (`#__PURE__`, `@__NO_SIDE_EFFECTS__`, `#` and `@` alike), Vite's
 * `@vite-ignore` on a dynamic import, and webpack's magic comments with
 * their values. Undefined for any other comment: it changes no build.
 */
export function buildComment(text: string): string | undefined {
  const found: string[] = [];
  if (/[#@]__PURE__/.test(text)) found.push("__PURE__");
  if (/[#@]__NO_SIDE_EFFECTS__/.test(text)) found.push("__NO_SIDE_EFFECTS__");
  if (/@vite-ignore\b/.test(text)) found.push("@vite-ignore");
  for (const m of text.matchAll(/\bwebpack[A-Z][A-Za-z]*\s*:\s*(?:"[^"]*"|'[^']*'|[^\s,*]+)/g))
    found.push(m[0].replace(/\s+/g, ""));
  return found.length ? found.join(" ") : undefined;
}

/**
 * A file's JSX pragmas, from the comments before its first token, where compilers read them: they
 * change how every element is built, so they belong to the module's init.
 */
export function jsxPragmas(text: string): string[] {
  const out: string[] = [];
  for (const r of ts.getLeadingCommentRanges(text, 0) ?? []) {
    const c = text.slice(r.pos, r.end);
    for (const m of c.matchAll(/@(jsx|jsxFrag|jsxImportSource|jsxRuntime)\s+([^\s*]+)/g))
      out.push(`@${m[1]} ${m[2]}`);
  }
  return out;
}

export interface CanonOptions {
  keepTypes?: boolean;
  /** Called with every identifier read in a value position. */
  onIdentifier?: (id: ts.Identifier) => void;
  /** Nodes to leave out (members that are units of their own). */
  skip?: Set<ts.Node>;
  /** Called with the start of every comment whose build text the form holds. */
  onComment?: (pos: number) => void;
}

const typeWrappers = new Set<ts.SyntaxKind>([
  K.AsExpression,
  K.SatisfiesExpression,
  K.NonNullExpression,
  K.TypeAssertionExpression,
  K.ExpressionWithTypeArguments,
]);

function isReadPosition(id: ts.Identifier): boolean {
  const p = id.parent;
  if (!p) return false;
  if (ts.isPropertyAccessExpression(p) && p.name === id) return false;
  if (ts.isQualifiedName(p) && p.right === id) return false;
  if (
    (ts.isPropertyAssignment(p) ||
      ts.isMethodDeclaration(p) ||
      ts.isPropertyDeclaration(p) ||
      ts.isGetAccessorDeclaration(p) ||
      ts.isSetAccessorDeclaration(p) ||
      ts.isEnumMember(p) ||
      ts.isPropertySignature(p) ||
      ts.isMethodSignature(p)) &&
    p.name === id
  )
    return false;
  if (ts.isBindingElement(p) && p.propertyName === id) return false;
  if (
    (ts.isVariableDeclaration(p) ||
      ts.isFunctionDeclaration(p) ||
      ts.isFunctionExpression(p) ||
      ts.isClassDeclaration(p) ||
      ts.isClassExpression(p) ||
      ts.isParameter(p) ||
      ts.isBindingElement(p) ||
      ts.isLabeledStatement(p) ||
      ts.isBreakOrContinueStatement(p) ||
      ts.isImportSpecifier(p) ||
      ts.isImportClause(p) ||
      ts.isNamespaceImport(p) ||
      ts.isEnumDeclaration(p) ||
      ts.isModuleDeclaration(p)) &&
    (p as { name?: ts.Node }).name === id
  )
    return false;
  if (ts.isJsxAttribute(p) && p.name === id) return false;
  if (ts.isMetaProperty(p)) return false;
  if (ts.isExportSpecifier(p) && p.name === id && p.propertyName !== undefined) return false;
  return true;
}

/**
 * Appends the canonical form of `node` to `out`: two pieces of code with the same form parse to the
 * same JavaScript.
 */
export function canon(node: ts.Node, out: string[], opt: CanonOptions = {}): void {
  const text = node.getSourceFile()?.text;
  // a comment is told once, at the outermost node it leads
  const told = new Set<number>();
  const comments = (n: ts.Node): void => {
    if (!text || n.kind === K.SourceFile) return;
    // TypeScript calls the comments on the trivia's first line trailing.
    const start = n.getStart();
    const ranges = [
      // one that ends the line before belongs to what came before
      ...(ts.getTrailingCommentRanges(text, n.pos) ?? []).filter(
        (r) => !text.slice(r.end, start).includes("\n"),
      ),
      ...(ts.getLeadingCommentRanges(text, n.pos) ?? []),
    ];
    for (const r of ranges) {
      if (r.end > start) continue;
      if (told.has(r.pos)) continue;
      told.add(r.pos);
      const c = buildComment(text.slice(r.pos, r.end));
      if (c) {
        out.push(` [c ${c}]`);
        opt.onComment?.(r.pos);
      }
    }
  };
  const visit = (n: ts.Node): void => {
    if (opt.skip?.has(n)) return;
    if (!opt.keepTypes) {
      if (typeWrappers.has(n.kind)) {
        visit((n as ts.AsExpression).expression);
        return;
      }
      if (isTypeOnly(n)) return;
      if (ts.isModifier(n) && typeOnlyModifiers.has(n.kind) && !ts.isParameter(n.parent)) return;
      // `x!` in a declaration and `x?` on a parameter or property
      if (n.kind === K.ExclamationToken || n.kind === K.QuestionToken) return;
    }
    if (n.kind === K.ParenthesizedExpression) {
      visit((n as ts.ParenthesizedExpression).expression);
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
        out.push(` ${(n as ts.Identifier).text}`);
        if (opt.onIdentifier && isReadPosition(n as ts.Identifier))
          opt.onIdentifier(n as ts.Identifier);
        break;
      case K.PrivateIdentifier:
        out.push(` ${(n as ts.PrivateIdentifier).text}`);
        break;
      case K.StringLiteral:
      case K.NumericLiteral:
      case K.BigIntLiteral:
        out.push(` ${JSON.stringify((n as ts.LiteralLikeNode).text)}`);
        break;
      case K.RegularExpressionLiteral:
        out.push(` ${JSON.stringify((n as ts.RegularExpressionLiteral).text)}`);
        break;
      case K.NoSubstitutionTemplateLiteral:
      case K.TemplateHead:
      case K.TemplateMiddle:
      case K.TemplateTail: {
        const t = n as ts.TemplateLiteralLikeNode;
        out.push(` ${JSON.stringify(t.rawText ?? t.text)}`);
        break;
      }
      case K.JsxText: {
        const t = jsxText((n as ts.JsxText).text);
        out.push(` ${JSON.stringify(t)}`);
        break;
      }
      case K.HeritageClause: {
        const h = n as ts.HeritageClause;
        out.push(` ${h.token}`);
        for (const t of h.types) visit(opt.keepTypes ? t : t.expression);
        out.push(")");
        return;
      }
    }
    if (n.kind === K.JsxText && (out[out.length - 1] ?? "") === ' ""') {
      // text that is only line breaks and indentation is not rendered
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

function isStatementList(n: ts.Node): boolean {
  return (
    ts.isBlock(n) ||
    ts.isSourceFile(n) ||
    ts.isModuleBlock(n) ||
    ts.isCaseClause(n) ||
    ts.isDefaultClause(n)
  );
}
