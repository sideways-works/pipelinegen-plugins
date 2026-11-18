# Contributing to the pipelinegen plugins

Thank you for wanting to help. This page says how to set up a working
copy, what every change has to pass, how commits are written and how a
plugin is released. What each plugin does is in its own README, which
also says what its tests need and how to run its conformance kit. The
protocol the plugins speak is in pipelinegen's book:
[plugins](https://github.com/sideways-works/pipelinegen/blob/main/docs/plugins.md)
and [writing plugins](https://github.com/sideways-works/pipelinegen/tree/main/docs/writing-plugins).

## Reporting a problem

Open an issue with the plugin, what you ran, what you expected and what
happened, and the version the plugin reports (`echo '{"protocol": 1}' |
go-sbom describe`, with the plugin's program). For a wrong answer, the
smallest tree that shows it and the request you sent say most of what we
need. A security problem goes through the private route in
[SECURITY.md](SECURITY.md), never an issue.

## A working copy

You need git and [Bazel](https://bazel.build) (bazelisk, which reads
`.bazelversion`). Bazel fetches the Go SDK, golangci-lint, Node and gh at
the versions and checksums `MODULE.bazel` pins, and every target runs
with them, whatever else your `PATH` holds.

```sh
git clone https://github.com/sideways-works/pipelinegen-plugins.git
cd pipelinegen-plugins
bazel run //tools:hooks           # the commit-msg hook below
```

`go.work` puts every Go module in one workspace for an editor, and
nothing else uses it: each target builds, tests and lints a module on
its own (`GOWORK=off`), as its release does, and a Go plugin that uses
another's library takes a released version of it, never the copy beside
it. The root's `package.json` makes the Node plugins one npm workspace
(see [Releases](#releases)).

## What every change passes

```sh
bazel run //<kind>/<name>:test    # the plugin's tests
bazel run //<kind>/<name>:lint    # its lint, with a cache of its own
bazel run //:lint                 # before a pull request: every lint and the checks below
```

- `:test` runs `go test ./...` in the plugin's module and in each module
  nested in it, or a Node plugin's `test` script after `npm ci`. Tests
  run plain: no `-race` and no `-count=1`. A test that needs the race
  detector says so in its own file.
- `:lint` runs golangci-lint with the root's `.golangci.yml` over each of
  the plugin's modules, or a Node plugin's `lint` script (Biome and
  `tsc`). Every finding fails it.
- `//:lint` also checks that `.tool-versions` and each Node plugin's
  `.node-version` name the versions `MODULE.bazel` pins, runs
  `pipelinegen check` on this repository's `pipelinegen.yaml`, and checks
  the subjects of the commits the change brings.
- A change to a template runs `bazel run //:templates`, which builds each
  template and runs pipelinegen's conformance kit on it.
- A change to what a plugin does comes with its docs: its README describes
  it as it is on `main`.

## Commits and pull requests

A commit message is one conventional subject line of at most 72
characters, such as `fix(driver/go): a bundle's tests see go test's
environment`, with no body and no trailers. The types are `feat`, `fix`,
`perf`, `refactor`, `test`, `docs`, `build`, `ci` and `chore`, and the
scope is the plugin's directory when the change is one plugin's. The
commit-msg hook refuses any other message. Keep commits small and each
one passing.

Open a pull request against `main`.

## Releases

Each plugin is released on its own, from a tag `<kind>/<name>/vX.Y.Z` on
`main`, by its `:release` target, which runs pipelinegen's release command
(at the version `tools/go.mod` requires) on the plugin's directory. The
command clones the tagged commit and builds the plugin in that directory
of the clone, so its programs record this repository's commit and their
version, and a nested module released with it is tagged
`<kind>/<name>/<sub>/vX.Y.Z` on the same commit. [RELEASING](RELEASING.md)
says in which order the plugins are released.

```sh
bazel run //<kind>/<name>:release -- build OUT [VERSION]
bazel run //<kind>/<name>:release -- notice
bazel run //<kind>/<name>:release -- publish VERSION OUT
bazel run //<kind>/<name>:release -- check VERSION
bazel run //<kind>/<name>:release -- patch VERSION OUT
bazel run //<kind>/<name>:release -- tagged OUT CONTEXT...
bazel run //:release -- build OUT
```

- `build` builds the tag `<kind>/<name>/VERSION` into `OUT` twice and fails
  unless the two builds give the same bytes; without `VERSION`, the
  plugin's newest tag on `HEAD`. A release holds the programs for linux
  and darwin on amd64 and arm64 (or one bundled `.mjs` for a Node
  plugin), `plugin.json`, the plugin's `LICENSE` and `NOTICE`, and
  `SHA256SUMS`, and the build refuses a `NOTICE` other than the one
  `notice` writes.
- `notice` writes the plugin's `NOTICE`: commit it before tagging.
- `publish` attaches `OUT` to the GitHub release of the tag, which must be
  pushed. A published release never changes: publishing it again succeeds
  only when it holds exactly this build.
- `check` builds the tag afresh and fails unless every file of its
  published release, a Node plugin's `.mjs` included, is byte for byte that
  build: a released file holds the code of its tag.
- `patch` tags `main`'s head (and each nested module), pushes the tags,
  builds, publishes and checks the release by download; run it again after
  a failure and it picks up where it stopped.
- `tagged` is a release job's form: the plugin's version tag on `HEAD`,
  once one of the commit statuses named passed on it.
- `//:release -- build OUT` builds every plugin whose newest tag is on
  `HEAD`, each into `OUT/<kind>/<name>`.

`PIPELINEGEN_SOURCE=<dir>` in the environment builds the release command
and pipelinegen from a checkout of pipelinegen instead, to try a version
not yet released.

A candidate is tagged `<kind>/<name>/vX.Y.Z-rc.N` in the same way and
published as a prerelease that is never marked the latest. A pushed tag
never moves; a wrong release gets a new version.

The Node plugins are one npm workspace, the root's `package.json`, with
one lockfile: `npm ci` anywhere in it installs every plugin's packages at
the root, and correlator/react takes ts-sbom from sbom/ts of the same
commit. A release still bundles each command into one `.mjs` of its own.

## Licence

The plugins are licensed under the [Apache License 2.0](LICENSE). By
contributing you agree that your contribution is licensed the same way.
