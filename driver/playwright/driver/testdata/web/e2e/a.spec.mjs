import { expect, test } from "./fixture.mjs";

test("adds", () => expect(1 + 1).toBe(2));
test.describe("words", () => {
  test("upper", () => expect("a".toUpperCase()).toBe("A"));
});
