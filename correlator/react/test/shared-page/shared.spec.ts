import { readFileSync } from "node:fs";
import { extname, join } from "node:path";
import type { Page } from "@playwright/test";
import { expect, test } from "../e2e/fixture.ts";

const dist = join(import.meta.dirname, "../../testdata/site/dist");
const types: Record<string, string> = { ".html": "text/html", ".js": "text/javascript" };

test.describe.configure({ mode: "serial" });

let page: Page;

test.beforeAll(async ({ browser }) => {
  page = await browser.newPage();
  await page.route("http://site.test/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const file = join(dist, path === "/" ? "index.html" : path);
    try {
      const contentType = types[extname(file)] ?? "application/octet-stream";
      await route.fulfill({ body: readFileSync(file), contentType });
    } catch {
      await route.fulfill({ status: 404 });
    }
  });
});

test.afterAll(async () => {
  await page.close();
});

test("opens the shop", async () => {
  await page.goto("http://site.test/");
  await page.locator("#add").waitFor();
});

// the same page, in the next test
test("opens the about page", async () => {
  await page.click("#about");
  await expect(page.locator("#about-text")).toHaveText("About us");
});
