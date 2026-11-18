import { expect, test } from "./fixture.mjs";

test("a build without the flag", async ({ page }) => {
  await page.goto("http://app.test/plain");
  await expect(page.locator("#out")).toHaveText("Welcome | 1 ticket | Hello, Welcome");
});
