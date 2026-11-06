export default {
  test: {
    include: ["app/**/*.test.mjs"],
    setupFiles: ["./recorder/vitest-setup.mjs"],
  },
};
