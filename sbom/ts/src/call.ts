// The calls answered without the TypeScript parser, which takes longer to load than these calls
// take: this module and its imports must never import it (test/command.test.ts checks).
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { candidates, type Detected, detect } from "./detect.ts";

export const name = "ts-sbom";
export const version = process.env.TS_SBOM_VERSION ?? "v0.0.0-dev";

export interface Request {
  protocol: number;
  work?: string;
  cache?: string;
  tree?: string;
  config?: Record<string, unknown>;
  /** Only these files, when the caller has the rest already. */
  paths?: string[];
  /** The files claimed for this plugin, the only files units reads. */
  claimed?: { path: string; role: string }[];
  narrowings?: string[];
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
}

export function parseRequest(input: string): Request {
  return (input.trim() ? JSON.parse(input) : { protocol: 1 }) as Request;
}

export function describe() {
  return {
    protocol: 1,
    kind: "sbom",
    name,
    version,
    namespaces: ["ts", "css"],
    capabilities: ["detect", "units"],
    candidates,
    inputs: { detect: "per-file", units: "per-file" },
    needs: ["node>=22.18"],
    narrowings: [],
  };
}

export function detectAll(req: Request): Detected[] {
  if (!req.tree) throw new Error("detect needs a tree");
  const out: Detected[] = [];
  for (const p of req.paths ?? []) {
    let text: string;
    try {
      text = readFileSync(join(req.tree, p), "utf8");
    } catch {
      continue;
    }
    const d = detect(p, text);
    if (d) out.push(d);
  }
  return out;
}

/** The answer to a call that needs no parser, or undefined for one that does. */
export function answerLight(call: string | undefined, req: Request): Answer | undefined {
  if (req.protocol !== 1)
    return { code: 2, out: { error: `protocol ${req.protocol} is not spoken here` } };
  for (const k of Object.keys(req.config ?? {}))
    if (goneKeys[k] && call !== "describe") return { code: 2, out: { error: goneKeys[k] } };
  switch (call) {
    case "describe":
      return { code: 0, out: describe() };
    case "detect":
      return { code: 0, out: { claims: detectAll(req) } };
    case "units":
      return undefined;
    default:
      return { code: 2, out: { error: `unknown call ${call ?? "(none)"}` } };
  }
}
