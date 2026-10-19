// The calls that need the TypeScript parser; in the built command this is the embedded part.
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { type Answer, answerLight, parseRequest, type Request } from "./call.ts";
import { label, scriptId, styleId } from "./ids.ts";
import { readTree } from "./tree.ts";

export { describe, detectAll, name, type Request, version } from "./call.ts";

export interface Unit {
  id: string;
  group: string;
  digest: string;
  file: string;
  kind: string;
  executable: boolean;
  /** For `pipelinegen why` only. */
  label: string;
}

/** The largest answer sent on stdout; a larger one goes into the work directory. */
export const maxInline = 32 << 20;

export function filesOf(req: Request): string[] {
  const claimed = (req.claimed ?? []).map((c) => c.path);
  if (!req.paths) return claimed;
  const want = new Set(req.paths);
  return claimed.filter((p) => want.has(p));
}

export function units(req: Request): Unit[] {
  return scan(req).units;
}

/**
 * The units, and the scripts whose units hold everything a test can observe of them as code. A
 * stylesheet is never among those: nothing here says what else a test can see of it.
 */
export function scan(req: Request): { units: Unit[]; complete: string[] } {
  if (!req.tree) throw new Error("units needs a tree");
  const out: Unit[] = [];
  const complete: string[] = [];
  for (const [file, s] of readTree(req.tree, filesOf(req))) {
    if ("css" in s) {
      out.push({
        id: styleId(file),
        group: file,
        digest: s.css,
        file,
        kind: "css",
        executable: false,
        label: label(styleId(file)),
      });
      continue;
    }
    for (const u of s.units)
      out.push({
        id: scriptId(file, u.key),
        group: file,
        digest: u.digest,
        file,
        kind: u.kind,
        executable: u.executable,
        label: label(scriptId(file, u.key)),
      });
    if (s.incomplete === undefined) complete.push(file);
  }
  return {
    units: out.sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0)),
    complete: complete.sort(),
  };
}

export function writeLines(work: string, file: string, rows: unknown[]): string {
  mkdirSync(work, { recursive: true });
  writeFileSync(join(work, file), rows.map((r) => `${JSON.stringify(r)}\n`).join(""));
  return file;
}

export function run(call: string | undefined, input: string): Answer {
  const req = parseRequest(input);
  const light = answerLight(call, req);
  if (light) return light;
  switch (call) {
    case "units": {
      const { units: us, complete } = scan(req);
      // Left out when empty, as the protocol's empty lists are.
      const whole = complete.length > 0 ? { complete } : {};
      // Only an answer too big for one message goes into a file.
      if (req.work && JSON.stringify(us).length > maxInline)
        return {
          code: 0,
          out: { files: [writeLines(req.work, "units.jsonl", us)], links: [], ...whole },
        };
      return { code: 0, out: { units: us, links: [], ...whole } };
    }
    default:
      return { code: 2, out: { error: `unknown call ${call ?? "(none)"}` } };
  }
}
