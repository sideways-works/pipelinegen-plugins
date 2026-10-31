import { note, test } from "./fixture.ts";

test.beforeAll(async () => {
  note("beforeAll", test.info());
});

test("one", async () => {
  note("test", test.info());
});

test("two", async () => {
  note("test", test.info());
});

test.afterAll(async () => {
  note("afterAll", test.info());
});
