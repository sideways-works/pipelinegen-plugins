import { expect, test } from "./fixture.ts";
import { open } from "./helpers.ts";

test("opens the about page", async ({ page }) => {
  await open(page);
  await page.click("#about");
  await expect(page.locator("#about-text")).toHaveText("About us");
});
