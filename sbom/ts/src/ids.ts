// How units are named. The correlator imports this module, so both name a unit the same way.

/** Script unit ids: `ts:<file>#<key>`. */
export const scriptNamespace = "ts";
/** Stylesheet unit ids: `css:<file>`. */
export const styleNamespace = "css";

export function scriptId(file: string, key: string): string {
  return `${scriptNamespace}:${file}#${key}`;
}

export function styleId(file: string): string {
  return `${styleNamespace}:${file}`;
}

export function parseId(id: string): { file: string; key?: string } | undefined {
  if (id.startsWith(`${styleNamespace}:`)) return { file: id.slice(styleNamespace.length + 1) };
  if (!id.startsWith(`${scriptNamespace}:`)) return undefined;
  const rest = id.slice(scriptNamespace.length + 1);
  const at = rest.indexOf("#");
  if (at < 0) return undefined;
  return { file: rest.slice(0, at), key: rest.slice(at + 1) };
}

const scriptExtensions = /\.(?:[mc]?[jt]s|[jt]sx)$/;

/** Declaration files hold only types. */
export function isScript(path: string): boolean {
  return scriptExtensions.test(path) && !/\.d\.[mc]?ts$/.test(path);
}

export function isStyle(path: string): boolean {
  return path.endsWith(".css");
}

export const maxLabel = 80;

/** From the source root (`src/`) on, else the last three parts. */
export function shortPath(file: string): string {
  const parts = file.split("/");
  const src = parts.lastIndexOf("src");
  if (src >= 0 && src < parts.length - 1) return parts.slice(src + 1).join("/");
  return parts.slice(-3).join("/");
}

export function clip(s: string): string {
  const r = [...s];
  if (r.length <= maxLabel) return s;
  const half = Math.floor((maxLabel - 1) / 2);
  return `${r.slice(0, half).join("")}…${r.slice(r.length - (maxLabel - 1 - half)).join("")}`;
}

/** A unit as a developer names it, for people only: nothing compares, hashes or caches it. */
export function label(id: string): string {
  const p = parseId(id);
  if (!p) return clip(id);
  const where = shortPath(p.file);
  if (p.key === undefined) return clip(`the stylesheet ${where}`);
  if (p.key === "init") return clip(`the module ${where}`);
  const colon = p.key.indexOf(":");
  const kind = colon < 0 ? "" : p.key.slice(0, colon);
  const name = colon < 0 ? p.key.replace(/^@/, "") : p.key.slice(colon + 1);
  const what = kind === "type" ? `type ${name}` : kind === "class" ? `class ${name}` : name;
  return clip(`${what} in ${where}`);
}
