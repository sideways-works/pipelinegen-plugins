// Whether a file's units hold everything a test can observe of it as code: every statement in some
// digest, and no comment a tool reads that no digest holds. Where it can't be sure, the answer is
// no: a file left out is planned as before.
import ts from "typescript";
import { buildComment } from "./canon.ts";

/**
 * JSDoc and TSDoc tags, which document code and change nothing a test runs,
 * and the type checker's own, which only the type check reads. A comment
 * holding any other `@word` is taken for a pragma a tool reads. `@license`
 * and `@preserve` are not here: they make a legal comment, which a bundler
 * keeps in its output.
 */
const docTags = new Set([
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
  "ts-expect-error",
]);

const jsxTags = new Set(["jsx", "jsxfrag", "jsximportsource", "jsxruntime"]);

const toolText: [RegExp, string][] = [
  [/^\/[/*]!/, "a legal comment, which the bundle keeps"],
  [/[@#]__[A-Z_]+__/, "an annotation a bundler reads"],
  [/^\/\/\/\s*<(?:reference|amd-)/, "a triple-slash directive"],
  [/[#@]\s*source(?:Mapping)?URL\s*=/, "a source map reference"],
  [/\b(?:c8|v8|istanbul)\s+ignore\b/, "a coverage directive"],
  [/\bnode:coverage\b/, "a coverage directive"],
  [/\beslint-env\b/, "an environment a tool reads"],
];

/** `@` at the start of a comment line or after a space or brace, and not a scope (`@scope/name`). */
const tagPattern = /(?:^|[\s*{(])@([A-Za-z][\w-]*)(?![\w/-])/g;

/**
 * Why a comment is one a tool reads that the digests don't hold, or undefined. `told`: a digest
 * holds its build text; `head`: it comes before the first token, where JSX pragmas count.
 */
export function toolComment(comment: string, told = false, head = false): string | undefined {
  const build = buildComment(comment);
  // What canon keeps of the comment: the rest of it is checked as text.
  let rest = comment;
  if (build !== undefined) {
    if (!told) return "a comment a bundler reads, outside every digest";
    rest = rest
      .replace(/[#@]__(?:PURE|NO_SIDE_EFFECTS)__/g, "")
      .replace(/@vite-ignore\b/g, "")
      .replace(/\bwebpack[A-Z][A-Za-z]*\s*:/g, "");
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
  return undefined;
}

export function commentsOf(sf: ts.SourceFile): { pos: number; text: string }[] {
  const text = sf.text;
  const seen = new Set<number>();
  const out: { pos: number; text: string }[] = [];
  const take = (ranges: ts.CommentRange[] | undefined) => {
    for (const r of ranges ?? []) {
      if (seen.has(r.pos)) continue;
      seen.add(r.pos);
      out.push({ pos: r.pos, text: text.slice(r.pos, r.end) });
    }
  };
  // Every comment is trivia in front of some node or token, or after the last one.
  const visit = (n: ts.Node) => {
    take(ts.getLeadingCommentRanges(text, n.pos));
    take(ts.getTrailingCommentRanges(text, n.pos));
    take(ts.getTrailingCommentRanges(text, n.end));
    for (const c of n.getChildren(sf)) visit(c);
  };
  visit(sf);
  return out.sort((a, b) => a.pos - b.pos);
}

export interface Coverage {
  /** Code no unit's digest holds whole, each said once. */
  gaps: string[];
  /** Where the comments start whose build text a digest holds. */
  told: Set<number>;
}

/** Why the file's units don't cover it whole (a byte order mark and a shebang count), or undefined. */
export function incomplete(sf: ts.SourceFile, errors: number, cov: Coverage): string | undefined {
  if (errors > 0) return `${errors} syntax error${errors === 1 ? "" : "s"}`;
  if (sf.text.startsWith("﻿")) return "a byte order mark";
  if (sf.text.startsWith("#!")) return "a shebang line";
  if (cov.gaps.length > 0) return cov.gaps[0];
  const head = new Set((ts.getLeadingCommentRanges(sf.text, 0) ?? []).map((r) => r.pos));
  for (const c of commentsOf(sf)) {
    const why = toolComment(c.text, cov.told.has(c.pos), head.has(c.pos));
    if (why) return why;
  }
  return undefined;
}
