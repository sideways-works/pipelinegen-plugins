// Checks that a released command's embedded part matches a fresh build of these sources. Check out
// the release's tag and run `npm ci` first.
//
//   node scripts/check-release.ts ts-sbom.mjs
import { readFileSync } from "node:fs";
import { embeddedBundle, unpack } from "../build.ts";

function fail(message: string): never {
  process.stderr.write(`check-release: ${message}\n`);
  process.exit(1);
}

const file = process.argv[2];
if (!file) fail("usage: node scripts/check-release.ts <released .mjs>");
let released: Buffer | undefined;
try {
  released = unpack(readFileSync(file, "utf8"));
} catch (e) {
  fail(`${file}: the embedded part can't be unpacked: ${e instanceof Error ? e.message : e}`);
}
if (!released) fail(`${file} has no embedded part`);
const { code } = await embeddedBundle();
const fresh = Buffer.from(code);
if (!released.equals(fresh)) {
  let at = 0;
  while (at < released.length && released[at] === fresh[at]) at++;
  fail(
    `${file}: the embedded part (${released.length} bytes) differs from a build of these sources (${fresh.length} bytes) from byte ${at}`,
  );
}
process.stdout.write(
  `${file}: the embedded part is byte for byte a build of these sources (${fresh.length} bytes)\n`,
);
