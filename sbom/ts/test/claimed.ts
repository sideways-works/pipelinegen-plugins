// The claims the core would send for a fixture tree: every file put through this plugin's detect.
import { readdirSync } from "node:fs";
import { join } from "node:path";
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
  return detectAll({ protocol: 1, tree, paths: filesUnder(tree) });
}
