import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { unhashed, withoutMap } from "../src/observe.ts";

function tree(files: Record<string, string>): string {
  const root = mkdtempSync(join(tmpdir(), "unmapped-"));
  for (const [p, text] of Object.entries(files)) {
    mkdirSync(dirname(join(root, p)), { recursive: true });
    writeFileSync(join(root, p), text);
  }
  return root;
}

const app = { dist: "web/dist", public: "web/public" };
const worker = "self.onmessage = () => {};\n";
const runtime = "var e=Object.create,t=Object.defineProperty;export{e as n,t as r};\n";

test("a bundler names a copy <name>-<hash><ext>", () => {
  assert.equal(unhashed("pdf.worker.min-Bx1_2abc.mjs"), "pdf.worker.min.mjs");
  assert.equal(unhashed("page-Dpd-ooer.js"), "page.js");
  assert.equal(unhashed("sw.js"), "sw.js");
});

test("a build's own output is no source of a copy", () => {
  const root = tree({ "web/dist/sw.js": worker, "web/dist/sw.js.map": "{}" });
  assert.equal(
    withoutMap(root, { dist: "web/dist" }, "web/dist/sw.js", worker, (p) => join(root, p)),
    "",
    "a script of the build itself, in a build with maps, is the bundler's",
  );
});

test("each kind of script without a map is told apart", () => {
  const root = tree({
    "web/dist/assets/index-AAAAAAAA.js": "import './x';\n",
    "web/dist/assets/index-AAAAAAAA.js.map": "{}",
    "web/public/sw.js": "self.addEventListener('fetch', () => {});\n",
    "web/src/tiles.worker.js": worker,
    "node_modules/.pnpm/pdfjs-dist@4.0.0/node_modules/pdfjs-dist/build/pdf.worker.min.mjs": `${worker}// pdf\n`,
  });
  const built = (p: string) => join(root, p);
  assert.equal(withoutMap(root, app, "web/dist/sw.js", "/* injected */", built), "");
  assert.equal(
    withoutMap(root, app, "web/dist/assets/tiles.worker-CCCCCCCC.js", worker, built),
    "file:web/src/tiles.worker.js",
  );
  assert.equal(
    withoutMap(
      root,
      app,
      "web/dist/assets/pdf.worker.min-BBBBBBBB.mjs",
      `${worker}// pdf\n`,
      built,
    ),
    "dep:npm:pdfjs-dist",
  );
  assert.equal(
    withoutMap(root, app, "web/dist/assets/rolldown-runtime-DDDDDDDD.js", runtime, built),
    "",
  );
});

test("in a build without any source map, a script without one can't be named", () => {
  const root = tree({ "web/dist/assets/index-AAAAAAAA.js": "import './x';\n" });
  assert.equal(
    withoutMap(root, app, "web/dist/assets/index-AAAAAAAA.js", "import './x';\n", (p) =>
      join(root, p),
    ),
    undefined,
  );
});
