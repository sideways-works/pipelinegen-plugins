// The calls answered without TypeScript and the linker, which take longer to load than these calls
// take, and the core calls describe and collect often: this module and its imports must never
// import them (test/command.test.ts checks).
import { readFileSync } from "node:fs";
import { isAbsolute, join, resolve } from "node:path";
import { candidates, type Detected, detectWith, isTsconfig } from "./detect.ts";
import type { LinkOptions } from "./links.ts";
import { narrowings } from "./narrowings.ts";
import type { ObserveConfig } from "./observe.ts";
import { doneFile, finished, partsOf, takePart } from "./record.ts";

export const name = "react-correlator";
export const version = process.env.REACT_CORRELATOR_VERSION ?? "v0.0.0-dev";

export type Config = LinkOptions & ObserveConfig;

export interface Request {
  protocol: number;
  work?: string;
  cache?: string;
  tree?: string;
  config?: Config;
  narrowings?: string[];
  slice?: string;
  dirs?: string[];
  action?: string;
  dir?: string;
  from?: string;
  to?: string;
  paths?: string[];
  /** collect: the suite runs a stack, so a slice must leave a recording. */
  stack?: boolean;
  /** The files claimed for this plugin, the only files links and observe read. */
  claimed?: { path: string; role: string }[];
}

/** Undefined when the request carries no claims at all. */
export function claimedPaths(req: Request): string[] | undefined {
  return req.claimed?.map((c) => c.path);
}

/** Refused config keys, with what replaced them. */
const goneKeys: Record<string, string> = {
  include:
    "config.include is no longer read: put include patterns on the plugin's entry in pipelinegen.yaml",
  exclude:
    "config.exclude is no longer read: put exclude patterns on the plugin's entry in pipelinegen.yaml",
};

export interface Answer {
  code: number;
  out?: unknown;
  err?: string;
}

const heavyCalls = new Set(["links", "observe", "why"]);

export function parseRequest(input: string): Request {
  return (input.trim() ? JSON.parse(input) : { protocol: 1 }) as Request;
}

/** Named from the task's working directory. */
export function at(p: string): string {
  return isAbsolute(p) ? p : resolve(p);
}

export function describe() {
  return {
    protocol: 1,
    kind: "correlator",
    name,
    version,
    namespaces: [],
    capabilities: ["detect", "links", "observe", "collect", "why"],
    // The fixture writes each directory itself; only a slice's end has something to check.
    collects: ["slice-end"],
    // The one config key whose value is a path observe reads at run time.
    observe_reads: ["build_root"],
    candidates,
    inputs: { detect: "per-file", links: "per-set" },
    needs: ["node>=22.18"],
    narrowings,
  };
}

function collect(req: Request): Answer {
  // A slice's end takes a later part into the core's directory for it and, when the suite runs a
  // stack, checks the part finished.
  if (!["setup-end", "slice-start", "slice-end"].includes(req.action ?? ""))
    return { code: 2, err: `unknown action ${req.action}` };
  // A later part of a slice: the fixture wrote it beside the first.
  if (req.action === "slice-end" && req.dir && req.slice) {
    try {
      takePart(at(req.dir), req.slice);
    } catch (e) {
      return { code: 1, err: (e as Error).message };
    }
  }
  if (req.action === "slice-end" && req.stack) {
    const dir = req.dir ? at(req.dir) : "";
    const parts = dir ? partsOf(dir) : [];
    if (parts.length === 0 || !parts.every(finished))
      return {
        code: 1,
        err: `${req.dir ?? "the slice's directory"} has no finished recording (no part, or a part without its ${doneFile} file): is the test runner's fixture installed, and did the spec file end before this call?`,
      };
  }
  return { code: 0, out: {} };
}

export function detectAll(
  req: Request,
  jsonc: (path: string, text: string) => unknown,
): Detected[] {
  if (!req.tree) throw new Error("detect needs a tree");
  const detected: Detected[] = [];
  for (const p of req.paths ?? []) {
    let text: string;
    try {
      text = readFileSync(join(req.tree, p), "utf8");
    } catch {
      continue;
    }
    const d = detectWith(p, text, jsonc);
    if (d) detected.push(d);
  }
  return detected;
}

/** The answer to a call that needs no TypeScript, or undefined for one that does. */
export function answerLight(call: string | undefined, req: Request): Answer | undefined {
  if (req.protocol !== 1) return { code: 2, err: `protocol ${req.protocol} is not spoken here` };
  for (const k of Object.keys(req.config ?? {}))
    if (goneKeys[k] && call !== "describe") return { code: 2, err: goneKeys[k] };
  switch (call) {
    case "describe":
      return { code: 0, out: describe() };
    case "collect":
      return collect(req);
    case "detect":
      if (!req.tree) throw new Error("detect needs a tree");
      if ((req.paths ?? []).some(isTsconfig)) return undefined;
      return {
        code: 0,
        out: {
          claims: detectAll(req, () => {
            throw new Error("a tsconfig needs TypeScript to read");
          }),
        },
      };
    default:
      if (call !== undefined && heavyCalls.has(call)) return undefined;
      return { code: 2, err: `unknown call ${call ?? "(none)"}` };
  }
}
