// Preloaded by test/command.test.ts with --import: writes `loaded <url>` to stderr for every module
// the process resolves.
import { registerHooks } from "node:module";

registerHooks({
  resolve(specifier, context, nextResolve) {
    const r = nextResolve(specifier, context);
    process.stderr.write(`loaded ${r.url}\n`);
    return r;
  },
});
