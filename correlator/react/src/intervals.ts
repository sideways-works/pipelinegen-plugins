// Covered code as disjoint intervals. V8 reports nested ranges; code ran when the innermost range
// holding it has a count above zero. Flattened, two snapshots merge by plain union.

/** [start, end) in UTF-16 units, and how often it ran. */
export interface Range {
  startOffset: number;
  endOffset: number;
  count: number;
}

export interface FunctionCoverage {
  ranges: Range[];
}

/** Sorted, disjoint, non-adjacent [start, end) pairs. */
export type Intervals = [number, number][];

export function ranIntervals(functions: FunctionCoverage[]): Intervals {
  const ranges: Range[] = [];
  for (const f of functions)
    for (const r of f.ranges) if (r.endOffset > r.startOffset) ranges.push(r);
  // Outer ranges first where two start together.
  ranges.sort((a, b) => a.startOffset - b.startOffset || b.endOffset - a.endOffset);
  const points = new Set<number>();
  for (const r of ranges) {
    points.add(r.startOffset);
    points.add(r.endOffset);
  }
  const ps = [...points].sort((a, b) => a - b);
  const out: Intervals = [];
  const stack: Range[] = [];
  let next = 0;
  for (let i = 0; i + 1 < ps.length; i++) {
    const a = ps[i] as number;
    const b = ps[i + 1] as number;
    while (stack.length && (stack[stack.length - 1] as Range).endOffset <= a) stack.pop();
    while (next < ranges.length && (ranges[next] as Range).startOffset <= a) {
      const r = ranges[next++] as Range;
      while (stack.length && (stack[stack.length - 1] as Range).endOffset <= r.startOffset)
        stack.pop();
      if (r.endOffset > a) stack.push(r);
    }
    const top = stack[stack.length - 1];
    if (top && top.count > 0) push(out, a, b);
  }
  return out;
}

function push(out: Intervals, a: number, b: number): void {
  const last = out[out.length - 1];
  if (last && last[1] >= a) last[1] = Math.max(last[1], b);
  else out.push([a, b]);
}

export function union(x: Intervals, y: Intervals): Intervals {
  const all = [...x, ...y].sort((a, b) => a[0] - b[0]);
  const out: Intervals = [];
  for (const [a, b] of all) push(out, a, b);
  return out;
}

export function contains(xs: Intervals, offset: number): boolean {
  let lo = 0;
  let hi = xs.length - 1;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    const [a, b] = xs[mid] as [number, number];
    if (offset < a) hi = mid - 1;
    else if (offset >= b) lo = mid + 1;
    else return true;
  }
  return false;
}
