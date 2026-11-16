# Contributing to react-correlator

Thank you for wanting to help. This page says how to set up a working
copy, what every change has to pass, and how commits are written. What
react-correlator records, how a run is recorded and how the command file
is built is in the [README](README.md). The protocol it speaks is in
pipelinegen's book:
[the plugin protocol](https://github.com/sideways-works/pipelinegen/blob/main/docs/plugins.md)
and [writing a correlator](https://github.com/sideways-works/pipelinegen/blob/main/docs/writing-plugins/correlator.md).

## Reporting a problem

Open an issue on the repository with what you ran, what you expected and
what happened, and the version react-correlator reports (`echo
'{"protocol": 1}' | node react-correlator.mjs describe`). For a spec file
that was bound too narrowly or too widely, the smallest site that shows
it, how it was built (bundler, source maps) and the `why` answer for the
slice in question say most of what we need. A security problem goes
through the private route in [SECURITY.md](SECURITY.md), never an issue.

## A working copy

You need Node at the version in `.node-version` (npm refuses another one),
npm, and git. ts-sbom, a dependency, is installed from its repository by
git, so git needs read access to
[it](https://github.com/sideways-works/pipelinegen-sbom-ts).

```sh
git clone https://github.com/sideways-works/pipelinegen-correlator-react.git
cd pipelinegen-correlator-react
npm ci
npx playwright install chromium   # the browser tests run a real Chromium
npm run build                     # dist/react-correlator.mjs and dist/playwright.mjs
```

## What every change passes

```sh
npm test
npm run lint                      # Biome and tsc --noEmit
npm run build
pipelinegen plugin test dist/react-correlator.mjs --tree testdata/tree --sbom <ts-sbom.mjs>
```

- The tests use no network: the browser tests serve the fixture sites in
  `testdata` through Playwright's routing, built by esbuild and by Vite
  with rolldown.
- `dist/playwright.mjs`, the fixture a repository's tests import, is
  committed: after a change to its sources, build it and commit it. A
  test fails when it differs from a fresh build, and so does the release.
- The last line is pipelinegen's conformance kit. It calls the built
  command the way pipelinegen does, on the fixture tree in
  `testdata/tree`, with ts-sbom's command beside it as `--sbom`, which says
  which claimed files hold units. Take `pipelinegen` from
  [its releases](https://github.com/sideways-works/pipelinegen/blob/main/docs/tutorial/install.md)
  and `ts-sbom.mjs` from a release of ts-sbom or its own build.
- `test/command.test.ts` fails when `describe`, `collect` or a `detect`
  without a tsconfig loads TypeScript: keep those calls in the small
  entry, `src/main.ts`.
- A change to what react-correlator records comes with its docs: the
  README describes react-correlator as it is on `main`.

## Commits and pull requests

A commit message is one conventional subject line of at most 72
characters, such as `fix: a script of another origin is foreign code,
whatever its path`, with no body and no trailers. The types are `feat`,
`fix`, `perf`, `refactor`, `test`, `docs`, `build`, `ci` and `chore`.
Keep commits small and each one passing.

Open a pull request against `main`.

## Releases

Releases are cut by the maintainers with pipelinegen's `ci/release.sh`,
as its [release guide](https://github.com/sideways-works/pipelinegen/blob/main/docs/releases.md)
describes, from a tag `vX.Y.Z` on `main`. The build uses the `node` on
your `PATH`, so it runs on the version in `.node-version`. The release
holds the command, the fixture `dist/playwright.mjs` (named in
`package.json` under `pipelinegen.release`), `LICENSE` and `NOTICE`. A candidate is tagged
`vX.Y.Z-rc.N` and published as a prerelease that is never marked the
latest. A published release never changes; a wrong one gets a new
version. `scripts/check-release.ts` checks that a released command holds
the code of its tag.

## Licence

react-correlator is licensed under the [Apache License 2.0](LICENSE). By
contributing you agree that your contribution is licensed the same way.
