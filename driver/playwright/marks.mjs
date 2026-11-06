// src/marks.ts
import { existsSync, readFileSync, renameSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { setTimeout as sleep } from "node:timers/promises";
var marksEnv = "PIPELINEGEN_MARKS";
var markSeconds = 120;
var stateKey = /* @__PURE__ */ Symbol.for("pipelinegen.marks");
function state() {
  const g = globalThis;
  g[stateKey] ??= { n: 0 };
  return g[stateKey];
}
async function exchange(dir, mark, slice, seconds) {
  const s = state();
  s.n++;
  const id = `${Date.now()}-${process.pid}-${s.n}`;
  const tmp = join(dir, `.${id}.tmp`);
  writeFileSync(tmp, JSON.stringify({ mark, slice }));
  renameSync(tmp, join(dir, `${id}.json`));
  const deadline = Date.now() + seconds * 1e3;
  for (; ; ) {
    if (existsSync(join(dir, `${id}.done`))) return;
    const failed = join(dir, `${id}.failed`);
    if (existsSync(failed))
      throw new Error(`pipelinegen mark ${mark} ${slice}: ${readFileSync(failed, "utf8")}`);
    if (Date.now() > deadline)
      throw new Error(
        `pipelinegen mark ${mark} ${slice}: the batch did not answer within ${seconds}s in ${dir}`
      );
    await sleep(20);
  }
}
async function markStart(slice, seconds = markSeconds) {
  const dir = process.env[marksEnv];
  if (!dir) return;
  const s = state();
  if (s.open === slice) return;
  if (s.open !== void 0) await markStop(s.open, seconds);
  await exchange(dir, "start", slice, seconds);
  s.open = slice;
}
async function markStop(slice, seconds = markSeconds) {
  const dir = process.env[marksEnv];
  if (!dir) return;
  const s = state();
  if (s.open !== slice) return;
  s.open = void 0;
  await exchange(dir, "stop", slice, seconds);
}
var onSliceStart = (slice) => markStart(slice);
var onSliceEnd = (slice) => markStop(slice);
export {
  markSeconds,
  markStart,
  markStop,
  marksEnv,
  onSliceEnd,
  onSliceStart
};
