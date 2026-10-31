import { note, test } from "./fixture.ts";

test("quick", async () => {
  note("test", test.info());
});

// the run's global timeout cuts it
test("slow", async () => {
  note("test", test.info());
  await new Promise((r) => setTimeout(r, 60_000));
});

test("never reached", async () => {
  note("test", test.info());
});
