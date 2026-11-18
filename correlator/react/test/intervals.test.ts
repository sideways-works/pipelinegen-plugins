import assert from "node:assert/strict";
import { test } from "node:test";
import { contains, ranIntervals, union } from "../src/intervals.ts";

test("the innermost range decides whether code ran", () => {
  const ran = ranIntervals([
    { ranges: [{ startOffset: 0, endOffset: 100, count: 1 }] },
    {
      ranges: [
        { startOffset: 10, endOffset: 50, count: 2 },
        { startOffset: 20, endOffset: 30, count: 0 },
      ],
    },
    { ranges: [{ startOffset: 60, endOffset: 70, count: 0 }] },
  ]);
  assert.deepEqual(ran, [
    [0, 20],
    [30, 60],
    [70, 100],
  ]);
  assert.ok(contains(ran, 0));
  assert.ok(!contains(ran, 25));
  assert.ok(!contains(ran, 65));
  assert.ok(contains(ran, 99));
  assert.ok(!contains(ran, 100));
});

test("nothing ran when the outer range never did", () => {
  assert.deepEqual(ranIntervals([{ ranges: [{ startOffset: 0, endOffset: 9, count: 0 }] }]), []);
});

test("snapshots merge by union", () => {
  assert.deepEqual(
    union(
      [
        [0, 5],
        [10, 15],
      ],
      [
        [4, 11],
        [20, 25],
      ],
    ),
    [
      [0, 15],
      [20, 25],
    ],
  );
});
