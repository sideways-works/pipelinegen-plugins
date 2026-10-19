// `ts-sbom <call>`: a JSON request on stdin, the answer on stdout. Only units loads the parser.
import { readFileSync } from "node:fs";
import { type Answer, answerLight, name, parseRequest } from "./call.ts";

let code = 1;
try {
  const call = process.argv[2];
  const input = call === "describe" ? "" : readFileSync(0, "utf8");
  const r: Answer =
    answerLight(call, parseRequest(input)) ?? (await import("./cli.ts")).run(call, input);
  if (r.out !== undefined) process.stdout.write(`${JSON.stringify(r.out)}\n`);
  code = r.code;
} catch (e) {
  process.stderr.write(`${name}: ${e instanceof Error ? e.message : String(e)}\n`);
}
process.exitCode = code;
