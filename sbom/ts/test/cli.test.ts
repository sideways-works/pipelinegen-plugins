import assert from "node:assert/strict";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { run, type Unit } from "../src/cli.ts";
import { claimedIn } from "./claimed.ts";

const tree = join(import.meta.dirname, "../testdata/app");
const claimed = claimedIn(tree);

test("describe names the plugin, its namespaces and what it needs", () => {
  const r = run("describe", "");
  assert.equal(r.code, 0);
  assert.deepEqual((r.out as { namespaces: string[] }).namespaces, ["ts", "css"]);
});

test("units lists every unit of the claimed files, sorted, without installed packages", () => {
  const r = run("units", JSON.stringify({ protocol: 1, tree, claimed }));
  assert.equal(r.code, 0);
  const units = (r.out as { units: Unit[] }).units;
  assert.deepEqual(
    units.map((u) => `${u.id} ${u.kind} ${u.executable}`),
    [
      "css:src/cart.css css false",
      "ts:src/cart.tsx#fn:Cart fn true",
      "ts:src/cart.tsx#fn:total fn true",
      "ts:src/cart.tsx#init init true",
      "ts:src/cart.tsx#type:Line type false",
      "ts:src/money.ts#const:CURRENCY const false",
      "ts:src/money.ts#fn:format fn true",
    ],
  );
  assert.ok(units.every((u) => u.group === u.file && /^[0-9a-f]{32}$/.test(u.digest)));
});

test("units names the scripts its units cover whole, never a stylesheet", () => {
  const r = run("units", JSON.stringify({ protocol: 1, tree, claimed }));
  assert.deepEqual((r.out as { complete: string[] }).complete, ["src/cart.tsx", "src/money.ts"]);
});

test("an answer within one message is sent on stdout, with a work directory too", () => {
  const work = mkdtempSync(join(tmpdir(), "ts-sbom-work-"));
  const req = JSON.stringify({ protocol: 1, tree, work, paths: ["src/money.ts"], claimed });
  const first = run("units", req) as { out: { units: Unit[]; links: unknown[] } };
  assert.deepEqual(
    first.out.units.map((u) => u.id),
    ["ts:src/money.ts#const:CURRENCY", "ts:src/money.ts#fn:format"],
  );
  assert.deepEqual(first.out.links, []);
  assert.deepEqual(run("units", req).out, first.out);
});

test("units reads only the claimed files", () => {
  const some = claimed.filter((c) => c.path === "src/money.ts");
  const r = run("units", JSON.stringify({ protocol: 1, tree, claimed: some }));
  assert.deepEqual(
    (r.out as { units: Unit[] }).units.map((u) => u.id),
    ["ts:src/money.ts#const:CURRENCY", "ts:src/money.ts#fn:format"],
  );
  // A claim from the include patterns may name any file; only scripts and stylesheets are read.
  const any = [
    ...some,
    { path: "node_modules/pkg/index.js", role: "included" },
    { path: "notes.md", role: "included" },
  ];
  const all = run("units", JSON.stringify({ protocol: 1, tree, claimed: any }));
  assert.equal((all.out as { units: Unit[] }).units.length, 3);
  const none = run("units", JSON.stringify({ protocol: 1, tree }));
  assert.deepEqual(none.out, { units: [], links: [] });
});

test("the include and exclude config keys are refused with where they went", () => {
  for (const key of ["include", "exclude"])
    for (const call of ["units", "detect"]) {
      const r = run(
        call,
        JSON.stringify({ protocol: 1, tree, claimed, config: { [key]: ["src"] } }),
      );
      assert.equal(r.code, 2);
      assert.match(
        (r.out as { error: string }).error,
        new RegExp(`config\\.${key} is no longer read: put ${key} patterns on the plugin's entry`),
      );
    }
});

test("an unknown call or protocol fails", () => {
  assert.equal(run("nope", JSON.stringify({ protocol: 1 })).code, 2);
  assert.equal(run("units", JSON.stringify({ protocol: 2, tree })).code, 2);
});

test("every unit carries a label for people, and nothing else depends on it", () => {
  const r = run("units", JSON.stringify({ protocol: 1, tree, claimed }));
  const units = (r.out as { units: Unit[] }).units;
  const byId = new Map(units.map((u) => [u.id, u.label]));
  assert.equal(byId.get("ts:src/cart.tsx#fn:Cart"), "Cart in cart.tsx");
  assert.equal(byId.get("ts:src/cart.tsx#init"), "the module cart.tsx");
  assert.equal(byId.get("ts:src/cart.tsx#type:Line"), "type Line in cart.tsx");
  assert.equal(byId.get("css:src/cart.css"), "the stylesheet cart.css");
  for (const u of units) {
    assert.ok(
      u.label && [...u.label].length <= 80 && !u.label.includes(tree),
      `${u.id}: ${u.label}`,
    );
  }
  const keys = Object.keys(units[0] ?? {}).sort();
  assert.deepEqual(keys, ["digest", "executable", "file", "group", "id", "kind", "label"]);
});

test("a label names a file from its source root on, and is clipped to 80 characters", async () => {
  const { label, shortPath } = await import("../src/ids.ts");
  assert.equal(shortPath("apps/web/src/entities/casefile/api.ts"), "entities/casefile/api.ts");
  assert.equal(shortPath("tools/scripts/gen/a/b.ts"), "gen/a/b.ts");
  assert.equal(
    label("ts:apps/web/src/entities/casefile/api.ts#fn:getCaseFile"),
    "getCaseFile in entities/casefile/api.ts",
  );
  assert.equal(label("ts:apps/web/src/x.ts#method:Store.get"), "Store.get in x.ts");
  const long = label(`ts:src/a.ts#fn:${"A".repeat(200)}`);
  assert.equal([...long].length, 80);
});
