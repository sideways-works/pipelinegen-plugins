// The fixture's config for failure files: a failed test keeps its
// screenshot and its trace.
export default {
  testDir: "e2e",
  testMatch: "*.spec.mjs",
  use: { screenshot: "only-on-failure", trace: "retain-on-failure" },
};
