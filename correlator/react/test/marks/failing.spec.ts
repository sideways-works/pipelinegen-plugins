import { expect } from "@playwright/test";
import { note, test } from "./fixture.ts";

test("fails", async () => {
  note("test", test.info());
  expect(1).toBe(2);
});

test("passes after", async () => {
  note("test", test.info());
});
