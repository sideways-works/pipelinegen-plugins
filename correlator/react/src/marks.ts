// The fixture's side of pipelinegen's marks: in a recorded stack bundle the spec files' tests say
// when each starts and stops. A mark is `<id>.json` written by rename into PIPELINEGEN_MARKS, taken
// once `<id>.done` appears, or refused with `<id>.failed`. The state lives on globalThis under the
// key marks.mjs uses, so a suite's own hooks and this fixture mark each boundary once.
import { existsSync, readFileSync, renameSync, statSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { setTimeout as sleep } from "node:timers/promises";

export const marksEnv = "PIPELINEGEN_MARKS";
/** As long as `pipelinegen slice mark` waits. */
export const markSeconds = 120;

interface MarkState {
  n: number;
  open?: string;
}

const stateKey = Symbol.for("pipelinegen.marks");

function state(): MarkState {
  const g = globalThis as { [stateKey]?: MarkState };
  g[stateKey] ??= { n: 0 };
  return g[stateKey];
}

/** pipelinegen makes the directory afresh for each marked bundle; a run it doesn't mark finds none. */
export function marksDir(): string | undefined {
  const dir = process.env[marksEnv];
  if (!dir) return undefined;
  try {
    return statSync(dir).isDirectory() ? dir : undefined;
  } catch {
    return undefined;
  }
}

async function exchange(dir: string, mark: string, slice: string, seconds: number): Promise<void> {
  const s = state();
  s.n++;
  // Ids sort in the order the marks were made.
  const id = `${Date.now()}-${process.pid}-${String(s.n).padStart(6, "0")}`;
  const tmp = join(dir, `.${id}.tmp`);
  writeFileSync(tmp, JSON.stringify({ mark, slice }));
  renameSync(tmp, join(dir, `${id}.json`));
  const deadline = Date.now() + seconds * 1000;
  for (;;) {
    if (existsSync(join(dir, `${id}.done`))) return;
    const failed = join(dir, `${id}.failed`);
    if (existsSync(failed))
      throw new Error(`pipelinegen mark ${mark} ${slice}: ${readFileSync(failed, "utf8")}`);
    if (Date.now() > deadline)
      throw new Error(
        `pipelinegen mark ${mark} ${slice}: the batch did not answer within ${seconds}s in ${dir}`,
      );
    await sleep(20);
  }
}

/**
 * A spec file still open is stopped first. It throws when the mark is refused or not taken in time,
 * so the spec file's tests don't run unmarked.
 */
export async function markStart(dir: string, slice: string, seconds = markSeconds): Promise<void> {
  const s = state();
  if (s.open === slice) return;
  if (s.open !== undefined) await markStop(dir, s.open, seconds);
  await exchange(dir, "start", slice, seconds);
  s.open = slice;
}

export async function markStop(dir: string, slice: string, seconds = markSeconds): Promise<void> {
  const s = state();
  if (s.open !== slice) return;
  s.open = undefined;
  await exchange(dir, "stop", slice, seconds);
}
