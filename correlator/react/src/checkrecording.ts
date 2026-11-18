// Finds the slices whose recordings hold scripts observe can't bind silently:
//
//   1. a Node script the commit doesn't hold;
//   2. a built browser script without a source map;
//   3. a map source inside the tree that the commit doesn't hold (listed only).
//
//   react-correlator check-recording --commit <rev> --coverage <dir> [--repo <dir>] [--build <dir>]...
//
// --coverage is the directory the recording run left; --build each directory a build was served
// from, for check 2. Exit 0: no slice affected; 1: record the listed slices again; 2: wrong use.
import { execFileSync } from "node:child_process";
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { join, posix, relative, sep } from "node:path";

interface Script {
  file: string;
  map?: { sources: string[]; sourceRoot?: string };
}

function args(argv: string[]): {
  commit: string;
  coverage: string;
  repo: string;
  builds: string[];
} {
  const out = { commit: "", coverage: "", repo: process.cwd(), builds: [] as string[] };
  for (let i = 0; i < argv.length; i += 2) {
    const v = argv[i + 1] ?? "";
    switch (argv[i]) {
      case "--commit":
        out.commit = v;
        break;
      case "--coverage":
        out.coverage = v;
        break;
      case "--repo":
        out.repo = v;
        break;
      case "--build":
        out.builds.push(v);
        break;
      default:
        throw new Error(`unknown argument ${argv[i]}`);
    }
  }
  if (!out.commit || !out.coverage) throw new Error("--commit and --coverage are needed");
  return out;
}

function parts(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    if (e.isDirectory()) parts(join(dir, e.name), out);
  }
  if (existsSync(join(dir, "done"))) out.push(dir);
  return out;
}

function files(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) files(p, out);
    else out.push(p);
  }
  return out;
}

// Bundlers name a copy <name>-<hash><ext>.
const byName = new Map<string, string[]>();
function copyOf(repo: string, file: string, js: string): string | undefined {
  if (byName.size === 0) {
    const visit = (dir: string) => {
      for (const e of readdirSync(join(repo, dir), { withFileTypes: true })) {
        const rel = dir ? `${dir}/${e.name}` : e.name;
        if (e.isDirectory()) {
          if (e.name !== ".git" && (!e.name.startsWith(".") || e.name === ".pnpm")) visit(rel);
        } else if (e.isFile()) byName.set(e.name, [...(byName.get(e.name) ?? []), rel]);
      }
    };
    visit("");
  }
  const base = file.split(sep).pop() ?? "";
  const m = /^(.+)-[A-Za-z0-9_-]{8}(\.[^.]+)$/.exec(base);
  const name = m ? `${m[1]}${m[2]}` : base;
  return (byName.get(name) ?? []).find((p) => {
    try {
      return readFileSync(join(repo, p), "utf8") === js;
    } catch {
      return false;
    }
  });
}

function inTree(p: string): boolean {
  return !p.startsWith("..") && !p.split("/").includes("node_modules") && !p.includes("\0");
}

export function checkRecording(argv: string[]): number {
  let o: ReturnType<typeof args>;
  try {
    o = args(argv);
  } catch (e) {
    process.stderr.write(`check-recording: ${(e as Error).message}\n`);
    return 2;
  }
  const tracked = new Set(
    execFileSync("git", ["-C", o.repo, "ls-tree", "-r", "--name-only", o.commit], {
      encoding: "utf8",
      maxBuffer: 1 << 28,
    })
      .split("\n")
      .filter(Boolean),
  );
  const built = o.builds.flatMap((b) => files(b));
  const found: string[] = [];
  let affected = 0;
  const say = (part: string, what: string, affects = true) => {
    if (affects) affected++;
    found.push(`${relative(o.coverage, part).split(sep).join(posix.sep)}: ${what}`);
  };

  for (const part of parts(o.coverage)) {
    const node = join(part, "node.json");
    if (existsSync(node)) {
      for (const s of (JSON.parse(readFileSync(node, "utf8")) as { scripts: Script[] }).scripts) {
        if (!tracked.has(s.file)) {
          say(
            part,
            s.map
              ? `node script ${s.file} is not in the commit: v0.1.3 dropped it; v0.1.4 reads it through its map`
              : `node script ${s.file} is not in the commit and has no map: v0.1.3 dropped it; v0.1.4 can't tell`,
          );
        }
        for (const src of s.map?.sources ?? []) {
          if (inTree(src) && !tracked.has(src))
            say(
              part,
              `note: map source ${src} (of ${s.file}) is not in the commit: binds nothing`,
              false,
            );
        }
      }
    }
    const browser = join(part, "browser.json");
    if (existsSync(browser) && built.length > 0) {
      for (const s of (JSON.parse(readFileSync(browser, "utf8")) as { scripts: { url: string }[] })
        .scripts) {
        let path: string;
        try {
          path = decodeURIComponent(new URL(s.url).pathname);
        } catch {
          continue;
        }
        const file = built.find((f) => f.split(sep).join(posix.sep).endsWith(path));
        if (!file || !statSync(file).isFile()) continue;
        const js = readFileSync(file, "utf8");
        const named = /\/\/[#@] sourceMappingURL=([^\s'"]+)\s*$/.exec(js.slice(-500))?.[1];
        const map = named && !/^[a-z]+:/i.test(named) ? join(file, "..", named) : `${file}.map`;
        if (named?.startsWith("data:")) continue;
        if (existsSync(map)) continue;
        const build = o.builds.find((b) => file.startsWith(b)) ?? "";
        const copy = copyOf(o.repo, file, js);
        if (!built.some((f) => f.startsWith(build) && /\.(c|m)?js\.map$/.test(f)))
          say(
            part,
            `browser script ${s.url} (${file}) has no source map, nor has any script of its build: v0.1.3 dropped it; v0.1.4 can't tell`,
          );
        else if (copy)
          say(
            part,
            `browser script ${s.url} (${file}) is a copy of ${copy} without a map: v0.1.3 dropped it; v0.1.4 binds ${copy.includes("node_modules/") ? "its package" : "that file"}`,
          );
        else
          say(
            part,
            `note: browser script ${s.url} (${file}) has no source map in a build that has them: the bundler's own code or a public file, binds nothing beyond its file`,
            false,
          );
      }
    }
  }
  for (const f of found) process.stdout.write(`${f}\n`);
  if (o.builds.length === 0)
    process.stderr.write(
      "check-recording: no --build given, so browser scripts' maps were not checked\n",
    );
  return affected > 0 ? 1 : 0;
}
