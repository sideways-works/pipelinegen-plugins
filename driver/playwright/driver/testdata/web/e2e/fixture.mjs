// A suite's own fixture, as a repository writes one: in a recording run it
// leaves each spec file's recording where pipelinegen says, and marks each
// spec file with the driver's marks module, which does nothing outside a
// recorded stack bundle.
import { mkdirSync, writeFileSync } from "node:fs";
import { join, relative } from "node:path";
import { test as base } from "@playwright/test";
// the driver's marks module, as a suite ships it beside its fixture
import { markStart, markStop } from "./marks.mjs";

let open;

export const test = base.extend({
  _worker: [
    async ({}, use) => {
      await use(undefined);
      if (open) await markStop(open);
    },
    { scope: "worker", auto: true },
  ],
  _spec: [
    async ({ _worker }, use, info) => {
      const slice = relative(process.env.PIPELINEGEN_TREE ?? process.cwd(), info.file);
      if (open !== slice) {
        await markStart(slice);
        open = slice;
      }
      if (process.env.PIPELINEGEN_COVERAGE) {
        const dir = join(process.env.PIPELINEGEN_COVERAGE, slice, "fake");
        mkdirSync(dir, { recursive: true });
        writeFileSync(join(dir, "ran.txt"), `ran ${slice}\n`);
      }
      await use(undefined);
    },
    { auto: true },
  ],
});

export { expect } from "@playwright/test";
