import { note, test } from "./fixture.ts";

// the run's global timeout cuts its beforeAll hook
test.beforeAll(async () => {
  note("beforeAll", test.info());
  await new Promise((r) => setTimeout(r, 60_000));
});

test("never reached", async () => {
  note("test", test.info());
});
