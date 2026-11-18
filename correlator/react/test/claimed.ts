// The claims the core would send for a fixture tree: every file put through this plugin's detect.
import { readdirSync } from "node:fs";
import { join } from "node:path";
import { readJsonc } from "../src/builds.ts";
import { detectAll } from "../src/call.ts";

export function filesUnder(root: string, dir = ""): string[] {
  return readdirSync(join(root, dir), { withFileTypes: true })
    .flatMap((e) => {
      const rel = dir ? `${dir}/${e.name}` : e.name;
      return e.isDirectory() ? filesUnder(root, rel) : [rel];
    })
    .sort();
}

export function claimedIn(tree: string): { path: string; role: string }[] {
  const claims = detectAll({ protocol: 1, tree, paths: filesUnder(tree) }, readJsonc);
  const out: { path: string; role: string }[] = claims.map((c) => ({ path: c.path, role: c.role }));
  const seen = new Set(out.map((c) => c.path));
  for (const c of claims)
    for (const d of c.depends ?? [])
      if (!seen.has(d)) {
        out.push({ path: d, role: "fragment" });
        seen.add(d);
      }
  return out.sort((a, b) => (a.path < b.path ? -1 : 1));
}

export function claimedPaths(tree: string): string[] {
  return claimedIn(tree).map((c) => c.path);
}
