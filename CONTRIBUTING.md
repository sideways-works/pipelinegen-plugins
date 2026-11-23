# Contributing to the pipelinegen plugins

Thank you for wanting to help. Below: setting up a working copy, the
layout, what every change has to pass, how to write commits, how to add
a plugin, and how a plugin is released. Each plugin's README says what it does, what its tests need
and how to run its conformance kit. The protocol the plugins speak is in
pipelinegen's book:
[plugins](https://github.com/sideways-works/pipelinegen/blob/main/docs/plugins.md)
and [writing plugins](https://github.com/sideways-works/pipelinegen/tree/main/docs/writing-plugins).

## Reporting a problem

Open an issue with the plugin, what you ran, what you expected and what
happened, and the version the plugin reports (`echo '{"protocol": 1}' |
go-sbom describe`, with the plugin's program). For a wrong answer, send
the smallest tree that shows it and the request you sent. Report a
security problem privately as [SECURITY.md](SECURITY.md) describes, never
in an issue.

## A working copy

You need git and [Bazel](https://bazel.build) (bazelisk, which reads
`.bazelversion`). Bazel fetches the Go SDK, golangci-lint, Node and gh at
the versions and checksums `MODULE.bazel` pins, and every target uses
those, whatever else is on your `PATH`.

```sh
git clone https://github.com/sideways-works/pipelinegen-plugins.git
cd pipelinegen-plugins
bazel run //tools:hooks           # the commit-msg hook below
```

`go.work` puts every Go module in one workspace for an editor. Nothing
else uses it: each target builds, tests and lints a module on its own
(`GOWORK=off`), as its release does, and a Go plugin that uses another's
library takes a released version of it, never the copy beside it. The root's `package.json` makes the Node plugins one npm workspace
(see [Releases](#releases)).

## Layout

```
<kind>/<name>/      a plugin: a Go module (github.com/sideways-works/pipelinegen-plugins/<kind>/<name>)
                    or a Node package, with its README, docs, LICENSE and the NOTICE its release needs
templates/<kind>/   a minimal plugin of each kind that passes pipelinegen's conformance kit
tools/              the launcher every bazel run target runs, and the commit-msg hook
pipelinegen.yaml    this repository's own CI: a suite per kind, each plugin's tests a slice
MODULE.bazel        the pinned Go SDK, golangci-lint, Node and gh, by checksum per platform
go.mod              the root module: no package, it names the repository for release versions
go.work             every module at once, for an editor; builds, tests and releases leave it off
package.json        the Node plugins' npm workspace, with one lockfile
```

Every plugin builds, tests and releases on its own, and none imports
another's code from this checkout. A plugin that uses another's library
(a correlator spelling its sbom's ids) requires a released version of it,
like any other module.

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

## Adding a plugin

1. Copy the template of its kind, `templates/<kind>`, to `<kind>/<name>`,
   and name the program `<name>-<kind>` (a driver named `rust` is
   `rust-driver`, under `cmd/rust-driver`).
2. Set the module path in `go.mod` to
   `github.com/sideways-works/pipelinegen-plugins/<kind>/<name>`, and the
   imports with it; add the module to `go.work`.
3. Make its `BUILD.bazel` the plugin's: `plugin(dir = "<kind>/<name>",
   tags = ["slice-plugins-<kind>"])` from `//tools:defs.bzl` in place of
   `template(...)`, which gives it `:test`, `:lint` and `:release`, and
   list it in `tools/units.bzl`.
4. Give it a README that says what it does and how to pin it, its `LICENSE`
   (the root's), and the `NOTICE` that
   `bazel run //<kind>/<name>:release -- notice` writes.

Its `:test` and `:lint` then become slices of this repository's own CI
([pipelinegen.yaml](pipelinegen.yaml)), in its kind's suite and the lint
suite, and run when its files change. `//:lint` fails until
`tools/units.bzl` lists it, and `pipelinegen check`, which `//:lint`
runs, fails while any file of the repository is in no slice.

A Node plugin is a package with `test`, `lint` and `build` scripts whose
build writes `dist/<program>.mjs`, `.node-version` naming the Node
`MODULE.bazel` pins, and a place in the root `package.json`'s
`workspaces`. [correlator/react](correlator/react) and [sbom/ts](sbom/ts)
are two of them.

## Releases

Each plugin is released on its own, from a tag `<kind>/<name>/vX.Y.Z` on
`main`, by its `:release` target, which runs pipelinegen's release command
(at the version `tools/go.mod` requires) on the plugin's directory. The
command clones the tagged commit and builds the plugin in that directory
of the clone, so its programs record this repository's commit and their
version, and a nested module released with it is tagged
`<kind>/<name>/<sub>/vX.Y.Z` on the same commit. [RELEASING](RELEASING.md)
says in which order the plugins are released.

A release has the same contents as before the plugins moved here: the
programs under their old names (`go-driver-linux-amd64`,
`react-correlator.mjs`), `plugin.json`, `LICENSE`, `NOTICE` and
`SHA256SUMS`. Every plugin's first release here is `v0.4.0`. A config
pins a release by `sums`, the checksum of its `SHA256SUMS`, so one pin
serves every platform; `sha256:` with one program's line
(`go-driver-linux-amd64`) in its place pins that file alone.

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
  build. So a released file holds the code of its tag.
- `patch` tags `main`'s head (and each nested module), pushes the tags,
  builds, publishes and checks the release by download; run it again after
  a failure and it picks up where it stopped.
- `tagged` is the form a release job uses: it takes the plugin's
  version tag on `HEAD`, once one of the named commit statuses passed on
  it.
- `//:release -- build OUT` builds every plugin whose newest tag is on
  `HEAD`, each into `OUT/<kind>/<name>`.

## Manifests

Each plugin's directory holds a `manifest.yaml`: its name and kind, its
program, a description, and how to build it, in a Go or Node image pinned
by digest, for the platforms a release has. `pipelinegen plugin build
<kind>/<name> --zip OUT` builds it into one zip that a config pins by its
URL and sha256, and a config that pins a commit builds it from the same
manifest. The Go build is the release's (`-trimpath`, `-s -w -buildid=`,
`CGO_ENABLED=0`, no `go.work`) with `-buildvcs=false`, and the modules come
from the Go module proxy, so the build has a network; it records neither a
commit nor a version, which only a release's tag gives. A Node build runs
`npm ci` and the plugin's `build` script in a copy of the repository, as
`npm ci` writes `node_modules`. The templates have a manifest each, which
`pipelinegen plugin new` renames with the rest.

`repository-manifest.yaml` at the root lists every plugin's manifest, and
its tags are `{path}/v{version}`, the release tags. A manifest's `files`
can't leave the plugin's directory, so each plugin keeps a copy of the
root's `LICENSE`: `//:checks` fails when a copy differs from the root's,
when a plugin has no manifest, or when the list misses one.

To try a pipelinegen that isn't released yet, set
`PIPELINEGEN_SOURCE=<dir>` in the environment: the release command and
pipelinegen are then built from that checkout of pipelinegen.

A candidate is tagged `<kind>/<name>/vX.Y.Z-rc.N` in the same way and
published as a prerelease that is never marked the latest. A pushed tag
never moves; a wrong release gets a new version.

The Node plugins are one npm workspace, the root's `package.json`, with
one lockfile. `npm ci` anywhere in it installs every plugin's packages at
the root, and correlator/react takes ts-sbom from sbom/ts of the same
commit. A release still bundles each command into its own `.mjs`.

## Licence

The plugins are licensed under the [Apache License 2.0](LICENSE). By
contributing you agree that your contribution is licensed the same way.
