import { test, expect } from "@playwright/test";

// Fails on purpose, with a page: its failure carries the page as it was.
test("finds a heading that is not there", async ({ page }) => {
  await page.setContent("<main><h1>Notes</h1><button>Add a note</button></main>");
  await expect(page.getByRole("heading", { name: "Tasks" })).toBeVisible({ timeout: 1000 });
});
