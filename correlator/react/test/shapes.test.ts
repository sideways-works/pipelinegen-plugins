// Source units and generated functions that don't line up one to one: a minifier inlines, merges,
// hoists and rewrites. Each chunk and map is built by hand, so the shape is exactly the one named.
import assert from "node:assert/strict";
import { test } from "node:test";
import { SourceMapGenerator } from "source-map-js";
import { buildIndex, functionsOf } from "../src/bundles.ts";
import { type FunctionCoverage, ranIntervals } from "../src/intervals.ts";
import { type Touched, touchedIn } from "../src/observe.ts";

const src = "src/a.ts";
const jsPath = "dist/a.js";

function position(text: string, offset: number): { line: number; column: number } {
  const before = text.slice(0, offset).split("\n");
  return { line: before.length, column: (before[before.length - 1] ?? "").length };
}

/** needle must occur exactly once. */
function at(text: string, needle: string): number {
  const i = text.indexOf(needle);
  assert.ok(i >= 0 && text.indexOf(needle, i + 1) < 0, `${needle} once in ${text}`);
  return i;
}

/** `ran` names the generated functions that ran by a needle at their start; `zero` the blocks that didn't. */
function observed(
  source: string,
  js: string,
  maps: [string, string][],
  ran: string[],
  zero: string[] = [],
): string[] {
  const gen = new SourceMapGenerator({ file: "a.js" });
  for (const [g, s] of maps)
    gen.addMapping({
      generated: position(js, at(js, g)),
      original: position(source, at(source, s)),
      source: "../src/a.ts",
    });
  const map = JSON.parse(gen.toString());
  const index = buildIndex(jsPath, js, map, (p) => (p === src ? source : undefined));
  const { fns, ends } = functionsOf(jsPath, js);
  const starts = new Set(ran.map((n) => at(js, n)));
  const coverage: FunctionCoverage[] = [
    { ranges: [{ startOffset: 0, endOffset: js.length, count: 1 }] },
  ];
  for (let i = 0; i < ends.length; i++) {
    const start = fns[i * 2] as number;
    const ranges = [
      { startOffset: start, endOffset: ends[i] as number, count: starts.has(start) ? 1 : 0 },
    ];
    for (const z of zero) {
      const s = at(js, z);
      if (s > start && s < (ends[i] as number))
        ranges.push({ startOffset: s, endOffset: s + z.length, count: 0 });
    }
    coverage.push({ ranges });
  }
  const reported = [0, ...[...starts].filter((s) => fns.some((f, k) => k % 2 === 0 && f === s))];
  const touched: Touched = { ran: new Set(), loaded: new Set() };
  touchedIn(index, ranIntervals(coverage), reported, touched);
  return [...touched.ran, ...touched.loaded].sort();
}

const id = (unit: string) => `ts:${src}#${unit}`;

test("a function inlined into its caller is touched when the caller ran", () => {
  const source =
    "export function double(x: number) {\n  return x * 2;\n}\nexport function next(y: number) {\n  return double(y) + 1;\n}\n";
  const js = "var z=1;function n(y){return y*2+1}export{n as next};";
  const maps: [string, string][] = [
    ["function n", "export function next"],
    ["y*2", "x * 2"],
    ["+1", "+ 1"],
  ];
  const ran = observed(source, js, maps, ["function n"]);
  assert.ok(ran.includes(id("fn:double")), ran.join(" "));
  assert.ok(ran.includes(id("fn:next")), ran.join(" "));
  const not = observed(source, js, maps, []);
  assert.ok(!not.includes(id("fn:double")), not.join(" "));
  assert.ok(!not.includes(id("fn:next")), not.join(" "));
});

test("two functions merged into one: each by the blocks of its own code that ran", () => {
  const source =
    "export function setX(a: { x?: number }) {\n  a.x = 1;\n}\nexport function setY(a: { y?: number }) {\n  a.y = 2;\n}\n";
  const js = "var z=1;function m(a,b){a.x=1;if(b){a.y=2}}export{m};";
  const maps: [string, string][] = [
    ["a.x=1", "a.x = 1"],
    ["a.y=2", "a.y = 2"],
  ];
  const both = observed(source, js, maps, ["function m"]);
  assert.ok(both.includes(id("fn:setX")) && both.includes(id("fn:setY")), both.join(" "));
  const one = observed(source, js, maps, ["function m"], ["{a.y=2}"]);
  assert.ok(one.includes(id("fn:setX")), one.join(" "));
  assert.ok(!one.includes(id("fn:setY")), one.join(" "));
});

test("a function hoisted into a wrapper: touched when it ran inside it", () => {
  const source = "export function one() {\n  return 1;\n}\n";
  const js = "var z=1;var w=()=>{function o(){return 1}return o()};export{w};";
  const maps: [string, string][] = [["return 1", "return 1"]];
  assert.ok(observed(source, js, maps, ["()=>{", "function o"]).includes(id("fn:one")));
  assert.ok(!observed(source, js, maps, []).includes(id("fn:one")));
  assert.ok(!observed(source, js, maps, ["()=>{"]).includes(id("fn:one")));
});

test("a class method turned into a property assignment", () => {
  const source = "export class Box {\n  size() {\n    return 3;\n  }\n}\n";
  const js = "var z=1;function B(){}B.prototype.size=function(){return 3};export{B as Box};";
  const maps: [string, string][] = [["return 3", "return 3"]];
  assert.ok(observed(source, js, maps, ["function(){return 3"]).includes(id("method:Box.size")));
  assert.ok(!observed(source, js, maps, []).includes(id("method:Box.size")));
});

test("an arrow passed inline: its code is its enclosing unit's, by its own range", () => {
  const source =
    "export function bump(xs: number[]) {\n  const n = xs.length;\n  return xs.map((x) => x + n);\n}\n";
  const js = "var z=1;function b(s){let e=s.length;return s.map(t=>t+e)}export{b as bump};";
  const maps: [string, string][] = [
    ["s.length", "xs.length"],
    ["t+e", "x + n"],
  ];
  assert.ok(observed(source, js, maps, ["function b"]).includes(id("fn:bump")));
  assert.ok(
    observed(source, js, [["t+e", "x + n"]], ["function b", "t=>"]).includes(id("fn:bump")),
  );
  assert.ok(!observed(source, js, [["t+e", "x + n"]], ["function b"]).includes(id("fn:bump")));
});

test("a default parameter's initialiser belongs to its function", () => {
  const source =
    "export function make() {\n  return 0;\n}\nexport function pick(a = make()) {\n  return a;\n}\n";
  const js = "var z=1;function p(a=k()){return a}function k(){return 0}export{p as pick};";
  const maps: [string, string][] = [
    ["k()){", "make()) {"],
    ["return a", "return a"],
    ["return 0", "return 0"],
  ];
  const r = observed(source, js, maps, ["function p", "function k"]);
  assert.ok(r.includes(id("fn:pick")) && r.includes(id("fn:make")), r.join(" "));
  const none = observed(source, js, maps, []);
  assert.ok(!none.includes(id("fn:pick")) && !none.includes(id("fn:make")), none.join(" "));
});

test("a function that exists only as a getter", () => {
  const source = "export class Pot {\n  get size() {\n    return 4;\n  }\n}\n";
  const js = "var z=1;class P{get size(){return 4}}export{P as Pot};";
  const maps: [string, string][] = [["return 4", "return 4"]];
  assert.ok(observed(source, js, maps, ["get size"]).includes(id("method:Pot.get size")));
  assert.ok(!observed(source, js, maps, []).includes(id("method:Pot.get size")));
});
