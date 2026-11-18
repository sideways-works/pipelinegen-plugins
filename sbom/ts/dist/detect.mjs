// src/ids.ts
var scriptExtensions = /\.(?:[mc]?[jt]s|[jt]sx)$/;
function isScript(path) {
  return scriptExtensions.test(path) && !/\.d\.[mc]?ts$/.test(path);
}
function isStyle(path) {
  return path.endsWith(".css");
}

// src/detect.ts
var candidates = [
  "**/*.ts",
  "**/*.tsx",
  "**/*.mts",
  "**/*.cts",
  "**/*.js",
  "**/*.jsx",
  "**/*.mjs",
  "**/*.cjs",
  "**/*.css"
];
var generatedHeader = /^(?:\s*(?:\/\/|\/\*+|\*|#)[^\n]*\n){0,5}?\s*(?:\/\/|\/\*+|\*)\s*(?:Code generated(?: by ([^\s.]+))?|@generated|This file (?:was|is) (?:auto(?:matically)?[- ])?generated(?: by ([^\s.]+))?|DO NOT EDIT)/i;
function isBuilt(path, text) {
  const firstLine = text.slice(0, text.indexOf("\n") >>> 0);
  return firstLine.length > 5e3 && /\.(?:m?js|cjs|css)$/.test(path);
}
function detect(path, text) {
  if (path.split("/").includes("node_modules")) return void 0;
  if (!isScript(path) && !isStyle(path)) return void 0;
  if (isBuilt(path, text)) return void 0;
  const g = generatedHeader.exec(text.slice(0, 2e3));
  if (g) {
    const tool = g[1] ?? g[2];
    return tool ? { path, role: "generated", tool } : { path, role: "generated" };
  }
  return { path, role: "source" };
}
export {
  candidates,
  detect,
  isBuilt
};
