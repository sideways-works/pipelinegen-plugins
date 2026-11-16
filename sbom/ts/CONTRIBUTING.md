# Contributing to ts-sbom

Thank you for wanting to help. This page says how to set up a working
copy, what every change has to pass, and how commits are written. What
ts-sbom lists, how its ids and digests are made, its library and how the
command file is built is in the [README](README.md). The protocol it
speaks is in pipelinegen's book:
[the plugin protocol](https://github.com/sideways-works/pipelinegen/blob/main/docs/plugins.md)
and [writing an sbom plugin](https://github.com/sideways-works/pipelinegen/blob/main/docs/writing-plugins/sbom.md).

## Reporting a problem

Open an issue on the repository with what you ran, what you expected and
what happened, and the version ts-sbom reports (`echo '{"protocol": 1}' |
node ts-sbom.mjs describe`). For a digest that changed when it shouldn't
have, or stayed when it should have changed, the smallest file that shows
it and the `units` request you sent say most of what we need. A security
problem goes through the private route in [SECURITY.md](SECURITY.md),
never an issue.

## A working copy

You need Node at the version in `.node-version` (npm refuses another one),
npm, and git.

```sh
git clone https://github.com/sideways-works/pipelinegen-sbom-ts.git
cd pipelinegen-sbom-ts
npm ci
npm run build                     # dist/ts-sbom.mjs, and the committed library files
```

## What every change passes

```sh
npm test
npm run lint                      # Biome and tsc --noEmit
npm run build
pipelinegen plugin test dist/ts-sbom.mjs --tree testdata/app
```

- The tests use no network; their trees are the fixtures in `testdata`.
- The library's built files, `dist/lib.mjs` and `dist/detect.mjs`, are
  committed, so that a package installing ts-sbom from git runs no
  script: after a change to their sources, build them and commit them. A
  test fails when they differ from a fresh build.
- The ids the library makes are what react-correlator names units by, so a
  change to an id's form is a change to both plugins.
- `test/command.test.ts` fails when `describe` or `detect` loads the
  parser: keep those calls in the small entry, `src/main.ts`.
- The last line is pipelinegen's conformance kit. It calls the built
  command the way pipelinegen does, on the fixture tree in `testdata/app`.
  Take `pipelinegen` from
  [its releases](https://github.com/sideways-works/pipelinegen/blob/main/docs/tutorial/install.md).
- A change to what ts-sbom lists comes with its docs: the README describes
  ts-sbom as it is on `main`.

## Commits and pull requests

A commit message is one conventional subject line of at most 72
characters, such as `fix: digests keep the comments a bundler reads`,
with no body and no trailers. The types are `feat`, `fix`, `perf`,
`refactor`, `test`, `docs`, `build`, `ci` and `chore`. Keep commits small
and each one passing.

Open a pull request against `main`.

## Releases

Releases are cut by the maintainers with pipelinegen's `ci/release.sh`,
as its [release guide](https://github.com/sideways-works/pipelinegen/blob/main/docs/releases.md)
describes, from a tag `vX.Y.Z` on `main`. The build uses the `node` on
your `PATH`, so it runs on the version in `.node-version`. The release
holds the command, `LICENSE` and `NOTICE`. A candidate is tagged
`vX.Y.Z-rc.N` and published as a prerelease that is never marked the
latest. A published release never changes; a wrong one gets a new
version. `scripts/check-release.ts` checks that a released command holds
the code of its tag.

## Licence

ts-sbom is licensed under the [Apache License 2.0](LICENSE). By
contributing you agree that your contribution is licensed the same way.
