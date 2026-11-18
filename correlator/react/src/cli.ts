// The calls that need TypeScript and the linker; in the built command this is the embedded part.
// Exit codes: 0 answered, 3 can't tell, anything else failed.
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { readJsonc } from "./builds.ts";
import {
  type Answer,
  answerLight,
  at,
  claimedPaths,
  detectAll,
  parseRequest,
  type Request,
} from "./call.ts";
import { type CannotTell as CannotTellRow, type Link, Linker } from "./links.ts";
import { CannotTell, observe } from "./observe.ts";

export {
  type Answer,
  type Config,
  describe,
  name,
  type Request,
  version,
} from "./call.ts";

/** The largest answer sent on stdout; a larger one goes into the work directory. */
export const maxInline = 32 << 20;

function deps(touched: string[]): string {
  const ids = touched.filter((u) => u.startsWith("dep:"));
  return ids.length
    ? `touched ${ids.length} units of installed packages, which an sbom of the lockfile must list: ${ids.slice(0, 8).join(", ")}${ids.length > 8 ? ", ..." : ""}`
    : "";
}

function writeLines(work: string, file: string, rows: unknown[]): string {
  mkdirSync(work, { recursive: true });
  writeFileSync(join(work, file), rows.map((r) => `${JSON.stringify(r)}\n`).join(""));
  return file;
}

function linker(req: Request): Linker {
  if (!req.tree) throw new Error("a tree is needed");
  return new Linker(req.tree, claimedPaths(req) ?? [], req.config ?? {}, req.cache);
}

function linksOf(req: Request): { links: Link[]; cannot_tell: CannotTellRow[] } {
  return linker(req).links();
}

/** The shortest chain of links from one unit to another, with every narrowing off. */
export function why(req: Request): { path: { unit: string; note: string }[] } {
  if (!req.from || !req.to) throw new Error("why needs from and to");
  const { links } = linksOf(req);
  const out = new Map<string, Link[]>();
  for (const l of links) {
    const xs = out.get(l.from) ?? [];
    xs.push(l);
    out.set(l.from, xs);
  }
  const prev = new Map<string, Link>();
  const queue = [req.from];
  const seen = new Set(queue);
  while (queue.length) {
    const u = queue.shift() as string;
    if (u === req.to) break;
    for (const l of out.get(u) ?? []) {
      if (seen.has(l.to)) continue;
      seen.add(l.to);
      prev.set(l.to, l);
      queue.push(l.to);
    }
  }
  if (!seen.has(req.to)) return { path: [] };
  const path: { unit: string; note: string }[] = [];
  for (let u = req.to; u !== req.from; ) {
    const l = prev.get(u) as Link;
    path.unshift({ unit: u, note: `${l.kind}${l.narrowing ? ` (${l.narrowing} off)` : ""}` });
    u = l.from;
  }
  path.unshift({ unit: req.from, note: "start" });
  return { path };
}

export function run(call: string | undefined, input: string): Answer {
  const req = parseRequest(input);
  const light = answerLight(call, req);
  if (light) return light;
  switch (call) {
    case "links": {
      const sets = linker(req).sets(req.paths);
      if (req.work && JSON.stringify(sets).length > maxInline)
        return { code: 0, out: { files: [writeLines(req.work, "sets.jsonl", sets)] } };
      return { code: 0, out: { sets } };
    }
    case "observe": {
      if (!req.tree) throw new Error("observe needs a tree");
      try {
        const r = observe(
          req.tree,
          (req.dirs ?? []).map(at),
          req.config ?? {},
          req.cache,
          claimedPaths(req),
        );
        const { missing, generated, whole, served, foreign, ...answer } = r;
        // Map sources the tree lacks and the bundler's own scripts have no units: say so, for people.
        const notes = [
          missing.length
            ? `${missing.length} map sources of scripts that ran are not in the tree, so what ran there binds nothing (first: ${missing.slice(0, 3).join(", ")})`
            : "",
          generated.length
            ? `${generated.length} built scripts without a source map hold only the bundler's own code and bind nothing (${generated.slice(0, 3).join(", ")})`
            : "",
          // Only an sbom that reads the lockfile lists packages as units.
          deps(answer.touched),
          // Say which build an origin went to, so a wrong one shows.
          served.length ? `origins: ${served.join(", ")}` : "",
          foreign.length
            ? `${foreign.reduce((n, f) => n + Number(/\((\d+)\)$/.exec(f)?.[1] ?? 0), 0)} scripts of other origins ran and bind nothing, as code no build of the tree holds: ${foreign.join(", ")}`
            : "",
          whole.length
            ? `one script loads the app's modules at once (${whole.slice(0, 2).join(", ")}): attribution is per function, but each module's load-time code (its init and top-level values) counts for every spec that loads the script; split routes into chunks loaded on demand to narrow it`
            : "",
        ].filter(Boolean);
        const err = notes.length ? notes.join("; ") : undefined;
        return { code: 0, out: answer, ...(err ? { err } : {}) };
      } catch (e) {
        if (e instanceof CannotTell) return { code: 3, err: e.message };
        throw e;
      }
    }
    case "detect":
      return { code: 0, out: { claims: detectAll(req, readJsonc) } };
    case "why":
      return { code: 0, out: why(req) };
    default:
      return { code: 2, err: `unknown call ${call ?? "(none)"}` };
  }
}
