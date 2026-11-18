import type { Page } from "@playwright/test";

export async function open(page: Page): Promise<void> {
  await page.goto("http://site.test/");
  await page.locator("#add").waitFor();
}

export function neverUsedHelper(): string {
  return "unused";
}
