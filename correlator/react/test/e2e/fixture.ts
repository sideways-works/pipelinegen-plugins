import { readFileSync, writeFileSync } from "node:fs";
import { extname, join } from "node:path";
import { test as base } from "@playwright/test";
import type * as Fixture from "../../src/playwright.ts";
import { startPart } from "./core-parts.ts";

// the built fixture, as a repository takes it from a release, so what it
// writes is checked against what observe reads
const { withCoverage } = (await import(
  new URL("../../dist/playwright.mjs", import.meta.url).href
)) as typeof Fixture;

const root = join(import.meta.dirname, "../..");
// the build it serves: the esbuild one, or another one named by SITE_DIST
const dist = process.env.SITE_DIST ?? join(root, "testdata/site/dist");
const types: Record<string, string> = {
  ".html": "text/html",
  ".js": "text/javascript",
  ".svg": "image/svg+xml",
};

const out = process.env.COVERAGE_OUT;
const core = process.env.CORE_PARTS;
if (core) {
  try {
    readFileSync(core);
  } catch {
    writeFileSync(core, JSON.stringify({ parts: {} }));
  }
}

// the site is served from its build through routing, so no server and no network
export const test = withCoverage(base, {
  out,
  root,
  node: true,
  ...(core && out ? { onSliceStart: (slice: string) => startPart(out, core, slice) } : {}),
}).extend({
  context: async ({ context }, use) => {
    await context.route("http://site.test/**", async (route) => {
      const path = new URL(route.request().url()).pathname;
      // MAPS_GONE: the server has no source maps, so the fixture can't keep them
      if (process.env.MAPS_GONE && path.endsWith(".map")) {
        await route.fulfill({ status: 404 });
        return;
      }
      // MAPS_HTML: a single-page server's fallback answers a map it doesn't
      // have with its page, and status 200
      if (process.env.MAPS_HTML && path.endsWith(".map")) {
        await route.fulfill({
          body: readFileSync(join(dist, "index.html")),
          contentType: "text/html",
        });
        return;
      }
      const file = join(dist, path === "/" ? "index.html" : path);
      try {
        await route.fulfill({
          body: readFileSync(file),
          contentType: types[extname(file)] ?? "application/octet-stream",
        });
      } catch {
        await route.fulfill({ status: 404 });
      }
    });
    await use(context);
  },
});
export { expect } from "@playwright/test";
