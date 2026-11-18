// `react-correlator <call>`: a JSON request on stdin, the answer on stdout. Only the calls that need
// TypeScript and the linker load them. Exit codes: 0 answered, 3 can't tell, anything else failed.
// `react-correlator check-recording ...` is no call of the protocol: it checks recordings made by
// v0.1.3 or earlier (src/checkrecording.ts).
import { readFileSync } from "node:fs";
import { type Answer, answerLight, name, parseRequest } from "./call.ts";

let code = 1;
try {
  const call = process.argv[2];
  if (call === "check-recording") {
    code = (await import("./checkrecording.ts")).checkRecording(process.argv.slice(3));
  } else {
    const input = call === "describe" ? "" : readFileSync(0, "utf8");
    const r: Answer =
      answerLight(call, parseRequest(input)) ?? (await import("./cli.ts")).run(call, input);
    if (r.out !== undefined) process.stdout.write(`${JSON.stringify(r.out)}\n`);
    if (r.err) process.stderr.write(`${name}: ${r.err}\n`);
    code = r.code;
  }
} catch (e) {
  process.stderr.write(`${name}: ${e instanceof Error ? e.message : String(e)}\n`);
}
process.exitCode = code;
