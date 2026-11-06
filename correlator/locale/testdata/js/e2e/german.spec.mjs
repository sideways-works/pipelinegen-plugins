import { expect, test } from "./fixture.mjs";

test("the German page falls back to English for a greeting", async ({ page }) => {
  await page.goto("http://app.test/app?lng=de");
  await expect(page.locator("#out")).toHaveText("Willkommen | 1 Ticket | Hello, Willkommen");
  // a second page load in the same test: its probes are recorded too
  await page.goto("http://app.test/app?lng=en");
  await expect(page.locator("#out")).toHaveText("Welcome | 1 ticket | Hello, Welcome");
});
