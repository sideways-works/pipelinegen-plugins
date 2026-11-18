import assert from "node:assert/strict";
import { test } from "node:test";
import { summarize } from "../src/units.ts";

function units(text: string, path = "src/a.tsx") {
  return Object.fromEntries(summarize(path, text).units.map((u) => [u.key, u]));
}
function digests(text: string, path = "src/a.tsx") {
  return Object.fromEntries(summarize(path, text).units.map((u) => [u.key, u.digest]));
}

test("top-level functions, components, constants, classes and types are units", () => {
  const u = units(`
import { memo } from "react";
export function total(xs: number[]) { return xs.reduce((a, b) => a + b, 0); }
export const Card = memo(function Card(props: { title: string }) { return <h2>{props.title}</h2>; });
export const add = (a: number, b: number) => a + b;
export const ROUTES = [{ path: "/", name: "home" }];
export const HANDLERS = { click: () => 1 };
const { a, b } = ROUTES[0] as never;
export interface Props { title: string }
export type Id = string;
export class Cart {
  items: number[] = [];
  get size() { return this.items.length; }
  static empty() { return new Cart(); }
  add = (n: number) => this.items.push(n);
  total(): number { return total(this.items); }
}
export default function () { return null; }
console.log("loaded");
`);
  assert.deepEqual(Object.keys(u).sort(), [
    "class:Cart",
    "const:HANDLERS",
    "const:ROUTES",
    "const:a,b",
    "fn:Card",
    "fn:add",
    "fn:default",
    "fn:total",
    "init",
    "method:Cart.add",
    "method:Cart.get size",
    "method:Cart.static empty",
    "method:Cart.total",
    "type:Id",
    "type:Props",
  ]);
  assert.equal(u["const:ROUTES"]?.executable, false, "plain data never runs as a function");
  assert.equal(u["const:HANDLERS"]?.executable, true, "a constant holding functions can run");
  assert.equal(u["type:Props"]?.executable, false);
  assert.deepEqual(u["method:Cart.total"]?.refs, ["total"]);
});

test("a comment, a reflow or another quote style keeps every digest", () => {
  const a = digests(`
import { x } from "./x";
// the sum
export function total(xs: number[]) {
  return xs.reduce((a, b) => a + b, 0); // fold
}
export const label = 'total: ' + x;
export const list = [1, 2, 3];
export function View() { return <p className="a">
    Hello world,
    again
  </p>; }
`);
  const b = digests(`
import { x } from './x'

/** The sum. */
export function total(xs: number[]) { return xs.reduce((a, b) => (a + b), 0) }

export const label = "total: " + x
export const list = [
  1,
  2,
  3,
]
export function View() {
  return (
    <p className='a'>Hello world, again</p>
  )
}
`);
  assert.deepEqual(b, a);
});

test("a type-only edit keeps the runtime digests and changes the type's", () => {
  const a = digests(`
export interface Props { title: string }
export function show(p: Props): string { return p.title as string; }
export class Box<T> implements Iterable<T> { private readonly v!: T; declare w: T; get(): T { return this.v; } }
`);
  const b = digests(`
export interface Props { title: string; subtitle?: string }
export function show(p: Props & { x?: number }): unknown { return p.title satisfies string; }
export class Box<T, U = T> implements Iterable<U> { v!: T; get(): T { return this.v!; } }
`);
  assert.equal(b["fn:show"], a["fn:show"]);
  assert.equal(b["method:Box.get"], a["method:Box.get"]);
  assert.equal(b["class:Box"], a["class:Box"]);
  assert.notEqual(b["type:Props"], a["type:Props"]);
});

test("a change to what code does changes its digest and no other", () => {
  const base = `
export function a() { return 1; }
export function b() { return x?.y; }
export function c() { return (p, q) => p + q; }
`;
  const a = digests(base);
  for (const [edit, unit] of [
    [base.replace("return 1", "return 2"), "fn:a"],
    [base.replace("x?.y", "x.y"), "fn:b"],
    [base.replace("p + q", "p - q"), "fn:c"],
  ] as const) {
    const b = digests(edit);
    for (const k of Object.keys(a)) {
      if (k === unit) assert.notEqual(b[k], a[k], `${unit} must change`);
      else assert.equal(b[k], a[k], `${k} must not change for an edit to ${unit}`);
    }
  }
});

test("parentheses that change meaning are kept", () => {
  const a = digests("export const f = () => (a?.b).c;");
  const b = digests("export const f = () => a?.b.c;");
  assert.notEqual(a["fn:f"], b["fn:f"]);
  const c = digests("export const g = () => [1, , 2];");
  const d = digests("export const g = () => [1, 2];");
  assert.notEqual(c["fn:g"], d["fn:g"], "a hole in an array is not a trailing comma");
});

test("constructor parameter properties are runtime code", () => {
  const a = digests("export class A { constructor(private x: number) {} }");
  const b = digests("export class A { constructor(x: number) {} }");
  assert.notEqual(a["method:A.constructor"], b["method:A.constructor"]);
});

test("what an import binding names is part of the code that reads it", () => {
  const a = digests(
    `import { a } from "./one";\nexport function f() { return a(); }\nexport function g() { return 1; }`,
  );
  const b = digests(
    `import { a } from "./two";\nexport function f() { return a(); }\nexport function g() { return 1; }`,
  );
  assert.notEqual(a["fn:f"], b["fn:f"]);
  assert.equal(a["fn:g"], b["fn:g"]);
  assert.notEqual(a.init, b.init, "loading another module runs other code on import");
});

test("overloads are one unit; their signatures are types", () => {
  const u = units(`
export function f(a: string): string;
export function f(a: number): number;
export function f(a: unknown) { return a; }
`);
  assert.deepEqual(Object.keys(u), ["fn:f"]);
  const v = units(`
export function f(a: string): string;
export function f(a: unknown) { return a; }
`);
  assert.equal(u["fn:f"]?.digest, v["fn:f"]?.digest);
});

test("imports, exports and re-exports are listed", () => {
  const s = summarize(
    "src/i.ts",
    `
import def, { a as b, type T } from "./a";
import * as ns from "pkg";
import "./side.css";
import type { U } from "./u";
export { b as c };
export { d } from "./d";
export * from "./e";
export * as f from "./f";
const lazy = () => import("./page");
export default lazy;
export function useIt() { return ns.thing(def); }
`,
  );
  assert.deepEqual(
    s.imports.map((i) => `${i.kind} ${i.specifier}${i.in ? ` in ${i.in}` : ""}`),
    [
      "type ./a",
      "static ./a",
      "static pkg",
      "side-effect ./side.css",
      "type ./u",
      "re-export ./d",
      "re-export ./e",
      "re-export ./f",
      "dynamic ./page in fn:lazy",
    ],
  );
  assert.deepEqual(s.star, ["./e"]);
  assert.deepEqual(
    s.exports.map((e) => `${e.name}=${e.key ?? `${e.from}:${e.imported}`}`),
    ["c=./a:a", "d=./d:d", "f=./f:*", "default=fn:lazy", "useIt=fn:useIt"],
  );
  assert.deepEqual(units(s ? "" : "").init, undefined);
  const u = Object.fromEntries(s.units.map((x) => [x.key, x]));
  assert.deepEqual(u["fn:useIt"]?.refs, ["def", "ns.thing"]);
});

test("ids never depend on positions", () => {
  const a = units("export function a() {}\nexport function b() {}");
  const b = units("export function b() {}\n\n\nexport function a() {}");
  assert.equal(a["fn:a"]?.digest, b["fn:a"]?.digest);
  assert.deepEqual(Object.keys(a).sort(), Object.keys(b).sort());
});

test("each top-level name is mapped to its unit, and type-only uses are kept apart", () => {
  const s = summarize(
    "src/t.ts",
    `
import type { Remote } from "./remote";
import { make } from "./make";
export interface Shape { r: Remote }
export const DEFAULT = { r: 1 };
export function area(s: Shape): number { return make(DEFAULT).r * (s as Shape).r.n; }
`,
  );
  assert.deepEqual(s.locals, { Shape: "type:Shape", DEFAULT: "const:DEFAULT", area: "fn:area" });
  const area = s.units.find((u) => u.key === "fn:area");
  assert.deepEqual(area?.refs, ["DEFAULT", "make"]);
  assert.deepEqual(area?.typeRefs, ["Shape"]);
});

test("a worker started from a module is listed with the unit that starts it", () => {
  const s = summarize(
    "src/w.ts",
    `export function start() { return new Worker(new URL("./tiles.worker.ts", import.meta.url), { type: "module" }); }
export function shared() { return new SharedWorker("./chat.worker.js"); }`,
  );
  assert.deepEqual(
    s.imports.map((i) => `${i.kind} ${i.specifier} in ${i.in}`),
    ["worker ./tiles.worker.ts in fn:start", "worker ./chat.worker.js in fn:shared"],
  );
});

test("a comment a bundler reads is part of the digest of the unit that holds it", () => {
  const base = `
import { make } from "./make";
export const store = make();
export function load(name: string) { return import(name); }
export function lazy() { return import("./page"); }
export function helper() { return 1; }
`;
  const before = digests(base);
  const changed = (from: string, to: string) => {
    const after = digests(base.replace(from, to));
    return Object.keys(after).filter((k) => after[k] !== before[k]);
  };
  // a call a minifier may now drop
  assert.deepEqual(changed("= make()", "= /*#__PURE__*/ make()"), ["const:store"]);
  assert.deepEqual(changed("= make()", "= /* @__PURE__ */ make()"), ["const:store"]);
  // a function whose calls a minifier may drop
  assert.deepEqual(
    changed("export function helper", "/* @__NO_SIDE_EFFECTS__ */ export function helper"),
    ["fn:helper"],
  );
  // a dynamic import the bundler leaves alone, and a chunk's name
  assert.deepEqual(changed("import(name)", "import(/* @vite-ignore */ name)"), ["fn:load"]);
  assert.deepEqual(changed('import("./page")', 'import(/* webpackChunkName: "page" */ "./page")'), [
    "fn:lazy",
  ]);
  assert.notEqual(
    digests(base.replace('import("./page")', 'import(/* webpackChunkName: "page" */ "./page")'))[
      "fn:lazy"
    ],
    digests(base.replace('import("./page")', 'import(/* webpackChunkName: "other" */ "./page")'))[
      "fn:lazy"
    ],
  );
  // `#` and `@` are one annotation; a comment no build reads changes nothing
  assert.equal(
    digests(base.replace("= make()", "= /*#__PURE__*/ make()"))["const:store"],
    digests(base.replace("= make()", "= /*@__PURE__*/ make()"))["const:store"],
  );
  assert.deepEqual(changed("= make()", "= /* made here */ make()"), []);
  assert.deepEqual(changed("\nimport", "\n// @ts-nocheck\nimport"), []);
  // a comment ending the line before is the line before's
  assert.deepEqual(
    changed('import { make } from "./make";', 'import { make } from "./make"; /*#__PURE__*/'),
    [],
  );
});

test("a file's JSX pragma is part of its init", () => {
  const base = "export function View() { return <p>hi</p>; }\n";
  const plain = digests(base);
  const preact = digests(`/** @jsxImportSource preact */\n${base}`);
  const emotion = digests(`/** @jsxImportSource @emotion/react */\n${base}`);
  assert.equal(plain["init"], undefined, "a file of declarations alone has no init");
  assert.ok(preact["init"], "a pragma gives it one");
  assert.notEqual(preact["init"], emotion["init"]);
  // the component's own digest is its code's: the pragma belongs to the file
  assert.equal(preact["fn:View"], plain["fn:View"]);
  assert.equal(digests(`// @jsx h\n${base}`)["fn:View"], plain["fn:View"]);
  assert.notEqual(
    digests(`// @jsx h\n${base}`)["init"],
    digests(`// @jsx createElement\n${base}`)["init"],
  );
});
