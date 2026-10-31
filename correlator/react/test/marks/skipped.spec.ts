import { note, test } from "./fixture.ts";

test.skip("skipped", async () => {
  note("test", test.info());
});
