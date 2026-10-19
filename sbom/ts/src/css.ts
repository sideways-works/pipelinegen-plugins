// A stylesheet's digest: its rules without comments or formatting whitespace.
import { hash } from "./units.ts";

export function normalizeCSS(text: string): string {
  let out = "";
  let i = 0;
  let space = false;
  const n = text.length;
  while (i < n) {
    const c = text[i] as string;
    if (c === "/" && text[i + 1] === "*") {
      const end = text.indexOf("*/", i + 2);
      i = end < 0 ? n : end + 2;
      space = true;
      continue;
    }
    if (c === '"' || c === "'") {
      let j = i + 1;
      while (j < n && text[j] !== c) j += text[j] === "\\" ? 2 : 1;
      if (space && out && !/[{};,>:(]$/.test(out)) out += " ";
      space = false;
      out += text.slice(i, j + 1);
      i = j + 1;
      continue;
    }
    if (/\s/.test(c)) {
      space = true;
      i++;
      continue;
    }
    // No space around braces, semicolons, commas and child combinators, or after a colon.
    if (space && out && !/[{};,>:(]$/.test(out) && !/[{};,>)]/.test(c)) out += " ";
    space = false;
    // A semicolon before a closing brace changes nothing.
    if (c === "}" && out.endsWith(";")) out = out.slice(0, -1);
    out += c;
    i++;
  }
  return out;
}

export function cssDigest(text: string): string {
  return hash(["css", normalizeCSS(text)]);
}
