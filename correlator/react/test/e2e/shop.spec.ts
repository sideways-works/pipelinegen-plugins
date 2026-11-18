import { expect, test } from "./fixture.ts";
import { open } from "./helpers.ts";

test("adds to the cart", async ({ page }) => {
  await open(page);
  await page.click("#add");
  await expect(page).toHaveTitle("3");
});
