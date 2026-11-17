import { test, expect } from "@playwright/test";

// Fails on purpose, with a page, beside page.spec.mjs: two spec files run
// one after another each keep their own failure files.
test("clicks a button that is not there", async ({ page }) => {
  await page.setContent("<main><h1>Shots</h1></main>");
  await expect(page.getByRole("button", { name: "Save" })).toBeVisible({ timeout: 1000 });
});
