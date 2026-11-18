import { defineConfig } from "@playwright/test";

// a spec file whose tests share one page, opened by its beforeAll hook
export default defineConfig({
  testDir: import.meta.dirname,
  workers: 1,
  reporter: "line",
  projects: [{ name: "desktop", use: { browserName: "chromium" } }],
});
