import { defineConfig } from "@playwright/test";

// spec files that need no browser, run as the playwright driver runs a
// recorded stack bundle: one worker, one spec file at a time
export default defineConfig({
  testDir: import.meta.dirname,
  workers: 1,
  fullyParallel: false,
  retries: 0,
  reporter: "line",
});
