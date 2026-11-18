import { appendFileSync } from "node:fs";
import { join, relative } from "node:path";
import { test as base, type TestInfo } from "@playwright/test";
import { markStart, markStop } from "../../src/marks.ts";
import type * as Fixture from "../../src/playwright.ts";

// the built fixture, as a repository takes it from a release
const { withCoverage } = (await import(
  new URL("../../dist/playwright.mjs", import.meta.url).href
)) as typeof Fixture;

const root = join(import.meta.dirname, "../..");

/** Notes what ran in the log the stand-in for pipelinegen writes its marks to. */
export function note(what: string, info: TestInfo): void {
  const log = process.env.MARKS_LOG;
  if (log) appendFileSync(log, `${what} ${relative(root, info.file)} ${info.title}\n`);
}

// a suite's own hooks marking as the playwright driver's marks.mjs does,
// through the same state on globalThis, as a suite set them up before the
// fixture marked by itself
const ownHooks = process.env.OWN_HOOKS
  ? {
      onSliceStart: (slice: string) => markStart(process.env.PIPELINEGEN_MARKS as string, slice),
      onSliceEnd: (slice: string) => markStop(process.env.PIPELINEGEN_MARKS as string, slice),
    }
  : {};

export const test = withCoverage(base, { out: process.env.COVERAGE_OUT, root, ...ownHooks });
