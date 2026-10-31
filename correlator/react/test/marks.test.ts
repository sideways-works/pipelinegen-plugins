// A stand-in for the running batch takes the marks as `pipelinegen slice run` does and writes them
// into the log the tests write what they ran to: every line of a spec file must lie between its own
// marks.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import {
  appendFileSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { finished, partsOf } from "../src/record.ts";

const root = join(import.meta.dirname, "..");
const spec = (name: string) => `test/marks/${name}.spec.ts`;

interface Mark {
  mark: string;
  slice: string;
}

/** refuse names the marks the stand-in fails. */
function standIn(dir: string, out: string, log: string, refuse?: (m: Mark) => string | undefined) {
  const seen = new Set<string>();
  const take = () => {
    const ids = readdirSync(dir)
      .filter((n) => n.endsWith(".json") && !n.startsWith("."))
      .map((n) => n.slice(0, -".json".length))
      .sort();
    for (const id of ids) {
      if (seen.has(id)) continue;
      seen.add(id);
      const m = JSON.parse(readFileSync(join(dir, `${id}.json`), "utf8")) as Mark;
      let line = `mark ${m.mark} ${m.slice}`;
      if (m.mark === "stop") {
        const parts = partsOf(join(out, m.slice, "react"));
        const last = parts[parts.length - 1];
        line += ` finished=${last !== undefined && finished(last)}`;
      }
      appendFileSync(log, `${line}\n`);
      const why = refuse?.(m);
      writeFileSync(join(dir, `${id}${why ? ".failed" : ".done"}`), why ?? "");
    }
  };
  const timer = setInterval(take, 10);
  return () => {
    clearInterval(timer);
    take();
  };
}

interface Run {
  code: number | null;
  output: string;
  lines: string[];
  out: string;
}

/** marks false runs without a stand-in; "missing" names a directory that doesn't exist. */
async function run(
  args: string[],
  opt: {
    marks?: boolean | "missing";
    env?: Record<string, string>;
    refuse?: (m: Mark) => string | undefined;
  } = {},
): Promise<Run> {
  const base = mkdtempSync(join(tmpdir(), "react-correlator-marks-"));
  const out = join(base, "coverage");
  const log = join(base, "log");
  const marks = join(base, "marks");
  writeFileSync(log, "");
  const env: Record<string, string | undefined> = {
    ...process.env,
    COVERAGE_OUT: out,
    MARKS_LOG: log,
    PIPELINEGEN_MARKS: undefined,
    ...opt.env,
  };
  let stop = () => {};
  if (opt.marks === "missing") env.PIPELINEGEN_MARKS = marks;
  else if (opt.marks !== false) {
    mkdirSync(marks);
    env.PIPELINEGEN_MARKS = marks;
    stop = standIn(marks, out, log, opt.refuse);
  }
  const child = spawn(
    "npx",
    ["playwright", "test", "-c", "test/marks/playwright.config.ts", ...args],
    {
      cwd: root,
      env,
    },
  );
  let output = "";
  child.stdout.on("data", (d) => {
    output += d;
  });
  child.stderr.on("data", (d) => {
    output += d;
  });
  const code = await new Promise<number | null>((resolve) => child.on("close", resolve));
  stop();
  const lines = readFileSync(log, "utf8").split("\n").filter(Boolean);
  return { code, output, lines, out };
}

const marksOf = (lines: string[]) => lines.filter((l) => l.startsWith("mark "));

function eachInsideItsMarks(lines: string[]): void {
  let open: string | undefined;
  for (const l of lines) {
    const [what, file] = l.split(" ");
    if (what === "mark") {
      const [, mark, slice] = l.split(" ");
      if (mark === "start") {
        assert.equal(open, undefined, `${l}: ${open} is still open\n${lines.join("\n")}`);
        open = slice;
      } else {
        assert.equal(open, slice, `${l}: ${open} is the one open\n${lines.join("\n")}`);
        open = undefined;
      }
      continue;
    }
    assert.equal(open, file, `${l}: ran while ${open} was open\n${lines.join("\n")}`);
  }
}

test("each spec file of a bundle is marked around all it runs, its part written first", async () => {
  const r = await run([spec("first"), spec("second")]);
  assert.equal(r.code, 0, r.output);
  assert.deepEqual(marksOf(r.lines), [
    `mark start ${spec("first")}`,
    `mark stop ${spec("first")} finished=true`,
    `mark start ${spec("second")}`,
    `mark stop ${spec("second")} finished=true`,
  ]);
  // The second file's beforeAll runs after its start, the first's afterAll before its stop.
  eachInsideItsMarks(r.lines);
  assert.ok(
    r.lines.some((l) => l.startsWith(`beforeAll ${spec("second")} `)),
    r.lines.join("\n"),
  );
  assert.ok(
    r.lines.some((l) => l.startsWith(`afterAll ${spec("first")} `)),
    r.lines.join("\n"),
  );
});

test("a suite's own hooks marking through marks.mjs's state leave one mark per boundary", async () => {
  const r = await run([spec("first"), spec("second")], { env: { OWN_HOOKS: "1" } });
  assert.equal(r.code, 0, r.output);
  assert.deepEqual(marksOf(r.lines), [
    `mark start ${spec("first")}`,
    `mark stop ${spec("first")} finished=true`,
    `mark start ${spec("second")}`,
    `mark stop ${spec("second")} finished=true`,
  ]);
  eachInsideItsMarks(r.lines);
});

test("a marked bundle with more than one worker is refused, and marks nothing", async () => {
  const r = await run([spec("first"), spec("second"), "--workers=2"]);
  assert.notEqual(r.code, 0);
  assert.match(r.output, /marked one at a time, so it runs with one worker, but this run has 2/);
  assert.deepEqual(marksOf(r.lines), []);
  assert.deepEqual(
    r.lines.filter((l) => l.startsWith("test ")),
    [],
    "no test ran",
  );
});

test("a spec file that fails goes on in a new worker, marked again: its parts add up", async () => {
  const r = await run([spec("failing"), spec("second")]);
  assert.equal(r.code, 1, r.output);
  assert.deepEqual(marksOf(r.lines), [
    `mark start ${spec("failing")}`,
    `mark stop ${spec("failing")} finished=true`,
    `mark start ${spec("failing")}`,
    `mark stop ${spec("failing")} finished=true`,
    `mark start ${spec("second")}`,
    `mark stop ${spec("second")} finished=true`,
  ]);
  eachInsideItsMarks(r.lines);
  assert.equal(partsOf(join(r.out, spec("failing"), "react")).length, 2);
});

test("a spec file whose tests are all skipped runs nothing and is not marked", async () => {
  const r = await run([spec("skipped"), spec("first")]);
  assert.equal(r.code, 0, r.output);
  assert.deepEqual(marksOf(r.lines), [
    `mark start ${spec("first")}`,
    `mark stop ${spec("first")} finished=true`,
  ]);
});

test("a spec file the run's global timeout cuts gets no stop and no finished part", async () => {
  const r = await run([spec("first"), spec("slow"), "--global-timeout=10000"]);
  assert.notEqual(r.code, 0, r.output);
  assert.deepEqual(marksOf(r.lines), [
    `mark start ${spec("first")}`,
    `mark stop ${spec("first")} finished=true`,
    `mark start ${spec("slow")}`,
  ]);
  eachInsideItsMarks(r.lines);
  const parts = partsOf(join(r.out, spec("slow"), "react"));
  assert.equal(parts.length, 1);
  assert.equal(finished(parts[0] as string), false);
  assert.ok(!r.lines.includes(`test ${spec("slow")} never reached`));
});

test("a spec file whose beforeAll hook the run's global timeout cuts gets no stop either", async () => {
  const r = await run([spec("first"), spec("slow-setup"), "--global-timeout=10000"]);
  assert.notEqual(r.code, 0, r.output);
  assert.deepEqual(marksOf(r.lines), [
    `mark start ${spec("first")}`,
    `mark stop ${spec("first")} finished=true`,
    `mark start ${spec("slow-setup")}`,
  ]);
  eachInsideItsMarks(r.lines);
  const parts = partsOf(join(r.out, spec("slow-setup"), "react"));
  assert.equal(parts.length, 1);
  assert.equal(finished(parts[0] as string), false);
});

test("a mark the batch refuses fails the spec file's tests before they run", async () => {
  const r = await run([spec("first"), spec("second")], {
    refuse: (m) =>
      m.mark === "start" && m.slice === spec("second") ? "no slice in this bundle" : undefined,
  });
  assert.equal(r.code, 1, r.output);
  assert.match(
    r.output,
    /pipelinegen mark start test\/marks\/second\.spec\.ts: no slice in this bundle/,
  );
  assert.deepEqual(
    r.lines.filter((l) => l.split(" ")[1] === spec("second") && !l.startsWith("mark ")),
    [],
  );
});

for (const marks of [false, "missing"] as const) {
  test(`without a marks directory (${marks === false ? "none named" : "named, not there"}) nothing is marked and the recording is as before`, async () => {
    const r = await run([spec("first"), spec("second")], { marks });
    assert.equal(r.code, 0, r.output);
    assert.deepEqual(marksOf(r.lines), []);
    for (const s of ["first", "second"]) {
      const parts = partsOf(join(r.out, spec(s), "react"));
      assert.equal(parts.length, 1);
      assert.ok(finished(parts[0] as string));
    }
  });
}
