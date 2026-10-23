// Where an app's build lives, found from its bundler config in the tree.
import { readFileSync } from "node:fs";
import { join, posix } from "node:path";
import ts from "typescript";
import { configName, type Detected, detectWith } from "./detect.ts";
import type { BundleConfig } from "./observe.ts";

export { candidates, type Detected } from "./detect.ts";

/** Read as TypeScript reads them (comments, trailing commas). */
export function readJsonc(path: string, text: string): unknown {
  return ts.parseConfigFileTextToJson(path, text).config;
}

export function detect(path: string, text: string): Detected | undefined {
  return detectWith(path, text, readJsonc);
}

export interface Build extends BundleConfig {
  config: string;
  /** The origin a full URL as base names, which serves the build. */
  origin?: string;
  /** Settings the config computes at run time, which the tree can't tell. */
  unknown: string[];
}

function configObject(sf: ts.SourceFile): ts.ObjectLiteralExpression | undefined {
  const fromExpr = (e: ts.Expression | undefined): ts.ObjectLiteralExpression | undefined => {
    if (!e) return undefined;
    while (ts.isParenthesizedExpression(e) || ts.isAsExpression(e) || ts.isSatisfiesExpression(e))
      e = e.expression;
    if (ts.isObjectLiteralExpression(e)) return e;
    if (ts.isCallExpression(e)) return fromExpr(e.arguments[0]);
    if (ts.isArrowFunction(e) || ts.isFunctionExpression(e)) {
      if (!ts.isBlock(e.body)) return fromExpr(e.body as ts.Expression);
      for (const st of e.body.statements)
        if (ts.isReturnStatement(st)) return fromExpr(st.expression);
    }
    if (ts.isIdentifier(e)) {
      for (const st of sf.statements)
        if (ts.isVariableStatement(st))
          for (const d of st.declarationList.declarations)
            if (ts.isIdentifier(d.name) && d.name.text === e.text) return fromExpr(d.initializer);
    }
    return undefined;
  };
  for (const st of sf.statements) if (ts.isExportAssignment(st)) return fromExpr(st.expression);
  return undefined;
}

function prop(o: ts.ObjectLiteralExpression, name: string): ts.Expression | undefined | null {
  for (const p of o.properties) {
    if (
      ts.isPropertyAssignment(p) &&
      (ts.isIdentifier(p.name) || ts.isStringLiteral(p.name)) &&
      p.name.text === name
    )
      return p.initializer;
    if (ts.isShorthandPropertyAssignment(p) && p.name.text === name) return null;
    if (ts.isSpreadAssignment(p)) return null; // a spread may set anything
  }
  return undefined;
}

/** A setting the config leaves out takes the bundler's default; one it computes is listed in `unknown`. */
export function buildOf(path: string, text: string, exists: (p: string) => boolean): Build {
  const sf = ts.createSourceFile(path, text, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
  const dir = posix.dirname(path);
  const unknown: string[] = [];
  const o = configObject(sf);
  const str = (
    obj: ts.ObjectLiteralExpression | undefined,
    name: string,
    label: string,
  ): string | undefined => {
    if (!obj) return undefined;
    const v = prop(obj, name);
    if (v === undefined) return undefined;
    if (v && (ts.isStringLiteral(v) || ts.isNoSubstitutionTemplateLiteral(v))) return v.text;
    unknown.push(label);
    return undefined;
  };
  if (!o) unknown.push("the config object");
  const rootSetting = str(o, "root", "root");
  const root = posix.normalize(posix.join(dir, rootSetting ?? "."));
  const baseSetting = str(o, "base", "base") ?? "/";
  // A relative base serves the same files under whatever path the page has.
  const base = /^https?:\/\//.test(baseSetting)
    ? new URL(baseSetting).pathname
    : baseSetting.startsWith("/")
      ? baseSetting
      : "/";
  const buildExpr = o ? prop(o, "build") : undefined;
  let outDir: string | undefined;
  if (buildExpr === null) unknown.push("build");
  else if (buildExpr && ts.isObjectLiteralExpression(buildExpr))
    outDir = str(buildExpr, "outDir", "build.outDir");
  else if (buildExpr) unknown.push("build");
  const publicDir = str(o, "publicDir", "publicDir");
  const html = posix.join(root, "index.html");
  const at = (p: string) => (p.startsWith("/") ? p.slice(1) : posix.normalize(posix.join(root, p)));
  return {
    config: path,
    dist: at(outDir ?? "dist"),
    base: base.endsWith("/") ? base : `${base}/`,
    ...(/^https?:\/\//.test(baseSetting) ? { origin: new URL(baseSetting).origin } : {}),
    public: at(publicDir ?? "public"),
    ...(exists(html) ? { html } : {}),
    unknown,
  };
}

export function findBuilds(root: string, files: string[]): Build[] {
  const set = new Set(files);
  const out: Build[] = [];
  for (const f of files) {
    if (!configName.test(f)) continue;
    let text: string;
    try {
      text = readFileSync(join(root, f), "utf8");
    } catch {
      continue;
    }
    if (!detect(f, text)) continue;
    out.push(buildOf(f, text, (p) => set.has(p)));
  }
  return out;
}
