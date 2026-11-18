import { spawnSync } from "node:child_process";
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const root = join(import.meta.dirname, "../..");

// With CORE_PARTS set, each slice's start does what pipelinegen's `slice
// start` does from a batch's hook: it closes the part running until then
// through the plugin's collect, into the directory the core gives that part
// (the first in <out>/<slice>, part k in <out>/.pipelinegen/part-k/<slice>),
// and counts the parts in a file, as the core's journal does, so a new
// worker per project keeps the count. The test closes the last part.
export function closePart(state: string): void {
  let s: { open?: string; dir?: string; parts: Record<string, number> } = { parts: {} };
  try {
    s = JSON.parse(readFileSync(state, "utf8"));
  } catch {
    // no part yet
  }
  if (!s.open || !s.dir) return;
  const r = spawnSync(process.execPath, [join(root, "src/main.ts"), "collect"], {
    input: JSON.stringify({
      protocol: 1,
      action: "slice-end",
      slice: s.open,
      dir: s.dir,
      stack: true,
    }),
    encoding: "utf8",
  });
  if (r.status !== 0) throw new Error(`collect ${s.open} into ${s.dir}: ${r.stderr}`);
  s.open = undefined;
  s.dir = undefined;
  writeFileSync(state, JSON.stringify(s));
}

export function startPart(out: string, state: string, slice: string): void {
  closePart(state);
  const s = JSON.parse(readFileSync(state, "utf8")) as { parts: Record<string, number> };
  const k = (s.parts[slice] ?? 0) + 1;
  s.parts[slice] = k;
  const dir =
    k === 1 ? join(out, slice, "react") : join(out, ".pipelinegen", `part-${k}`, slice, "react");
  writeFileSync(state, JSON.stringify({ ...s, open: slice, dir }));
}
