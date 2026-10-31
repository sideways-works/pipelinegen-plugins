import { defineConfig } from "@playwright/test";

// every spec file runs once per project, as a suite with a phone and a
// desktop layout does: each run is a part of the same slice
export default defineConfig({
  testDir: import.meta.dirname,
  workers: 1,
  fullyParallel: false,
  reporter: "line",
  projects: [
    { name: "phone", use: { browserName: "chromium", viewport: { width: 390, height: 844 } } },
    { name: "desktop", use: { browserName: "chromium", viewport: { width: 1280, height: 800 } } },
  ],
});
