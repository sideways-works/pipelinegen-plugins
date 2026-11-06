// A spec file a run stops before its end, for the driver's own test: its
// first test outlasts the run's global timeout, its second never starts.
import { expect, test } from "@playwright/test";

test("outlasts the run", async () => {
  await new Promise((done) => setTimeout(done, 20_000));
  expect(1).toBe(1);
});
test("never starts", () => expect(1).toBe(1));
