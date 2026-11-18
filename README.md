# pipelinegen plugins

The plugins of [pipelinegen](https://github.com/sideways-works/pipelinegen),
which runs only the test slices a change can break, in one repository:
the sbom plugins that say what exists, the correlators that say who uses
it, the drivers that run a suite's tests, the runner that writes the CI
pipeline and the connectors that talk to a git host. Each plugin is its
own Go module or Node package, with its own README, its own releases and
its own history, and a template per kind starts a new one.

| Plugin | Program | What it does |
|---|---|---|
| [connector/git](connector/git) | `git-connector` | changes from a plain git server's branches |
| [connector/github](connector/github) | `github-connector` | pull requests, statuses and comments on GitHub |
| [correlator/go](correlator/go) | `go-correlator` | who uses which Go declaration, and what a Go test ran |
| [correlator/gorm](correlator/gorm) | `gorm-correlator` | which PostgreSQL tables Go code reaches through GORM |
| [correlator/locale](correlator/locale) | `locale-correlator` | which message catalog keys a test looked up |
| [correlator/oapi-codegen](correlator/oapi-codegen) | `oapi-codegen-correlator` | which API operations oapi-codegen's generated code serves |
| [correlator/react](correlator/react) | `react-correlator` | who uses and who runs which unit of a TypeScript frontend |
| [correlator/sql](correlator/sql) | `sql-correlator` | which database objects a test's statements touched |
| [driver/bazel](driver/bazel) | `bazel-driver` | runs slices that are Bazel test targets |
| [driver/go](driver/go) | `go-driver` | runs slices that are Go test files |
| [driver/playwright](driver/playwright) | `playwright-driver` | runs slices that are Playwright spec files |
| [driver/shell](driver/shell) | `shell-driver` | runs a suite's steps written as shell |
| [runner/concourse](runner/concourse) | `concourse-runner` | writes the Concourse pipeline |
| [sbom/go](sbom/go) | `go-sbom` | a Go repository's declarations and modules |
| [sbom/locale](sbom/locale) | `locale-sbom` | the keys of a repository's message catalogs |
| [sbom/npm](sbom/npm) | `npm-sbom` | the packages an npm or pnpm workspace installs |
| [sbom/openapi](sbom/openapi) | `openapi-sbom` | an OpenAPI contract's operations and components |
| [sbom/pg](sbom/pg) | `pg-sbom` | the PostgreSQL database a repository's migrations build |
| [sbom/ts](sbom/ts) | `ts-sbom` | the functions, components and styles of TypeScript, JavaScript and CSS |

## Layout

```
<kind>/<name>/      a plugin: a Go module (github.com/sideways-works/pipelinegen-plugins/<kind>/<name>)
                    or a Node package, with its README, docs, LICENSE and the NOTICE its release needs
templates/<kind>/   a minimal plugin of each kind that passes pipelinegen's conformance kit
tools/              the launcher every bazel run target runs, and the commit-msg hook
MODULE.bazel        the pinned Go SDK, golangci-lint, Node and gh, by checksum per platform
go.work             every module at once, for an editor; builds, tests and releases leave it off
```

Every plugin builds, tests and releases on its own: no plugin imports
another's code from this checkout. A plugin that uses another's library
(a correlator spelling its sbom's ids) requires a released version of it,
as any other module.

## Everyday commands

The commands run with the tools `MODULE.bazel` pins and need only
[Bazel](https://bazel.build) (bazelisk reads `.bazelversion`):

```sh
bazel run //driver/go:test        # one plugin's tests: go test in each of its modules, or npm test
bazel run //driver/go:lint        # its lint: golangci-lint with .golangci.yml, or the package's lint script
bazel run //driver/go:release -- build OUT v0.2.1   # its release, built twice and compared (CONTRIBUTING)
bazel run //:test                 # every plugin's, template's and the tools' tests
bazel run //:lint                 # every lint, the tool versions, pipelinegen check and the commit subjects
bazel run //:templates            # every template built and run through pipelinegen's conformance kit
bazel run //tools:hooks           # installs the commit-msg hook in your checkout
```

A run never stops at the first plugin that fails: it runs them all and
names the ones that failed at the end. Each plugin's README says what its
tests need beyond that, and how to run its conformance kit by hand.

## Using a plugin

A repository pins each plugin it uses in its `pipelinegen.yaml`, by
release and checksum. The plugins of this repository are pinned under one
entry, each by its directory and its release:

```yaml
plugins:
  - repository: https://github.com/sideways-works/pipelinegen-plugins
    with:
      - {name: go, kind: driver, path: driver/go, release: {version: v0.2.1, sha256: ...}}
      - {name: go-sbom, kind: sbom, path: sbom/go, release: {version: v0.2.1, sha256: ...}}
```

`version` is the plugin's own version: the release tagged
`driver/go/v0.2.1` here. The checksum is the line of the release's
`SHA256SUMS` for the platform's program (`go-driver-linux-amd64`), or,
as `sums`, the checksum of `SHA256SUMS` itself. A plugin's README shows
its settings.

## Releases

Each plugin is released on its own, from a tag `<kind>/<name>/vX.Y.Z` on
`main`, and a nested module released with it (`correlator/go/cover`) gets
`<kind>/<name>/<sub>/vX.Y.Z` on the same commit. A release holds what it
held before the plugins moved here: the programs, named as before
(`go-driver-linux-amd64`, `react-correlator.mjs`), `plugin.json`,
`LICENSE`, `NOTICE` and `SHA256SUMS`. [CONTRIBUTING](CONTRIBUTING.md#releases)
says how one is cut.

## Adding a plugin

1. Copy the template of its kind, `templates/<kind>`, to `<kind>/<name>`,
   and name the program `<name>-<kind>` (a driver named `rust` is
   `rust-driver`, under `cmd/rust-driver`).
2. Set the module path in `go.mod` to
   `github.com/sideways-works/pipelinegen-plugins/<kind>/<name>`, and the
   imports with it; add the module to `go.work`.
3. Make its `BUILD.bazel` the plugin's: `plugin()` from `//tools:defs.bzl`
   in place of `template()`, which gives it `:test`, `:lint` and
   `:release`.
4. Give it a README that says what it does and how to pin it, its `LICENSE`
   (the root's), and the `NOTICE` that
   `bazel run //<kind>/<name>:release -- notice` writes.
5. Give its tests a slice in `pipelinegen.yaml` (the `slice-<kind>`
   filegroups in its `BUILD.bazel`): `pipelinegen check`, part of `//:lint`,
   refuses a test in no slice.

A Node plugin is a package with `test`, `lint` and `build` scripts whose
build writes `dist/<program>.mjs`, and `.node-version` naming the Node
`MODULE.bazel` pins; [correlator/react](correlator/react) and
[sbom/ts](sbom/ts) are two.

## Licence

The plugins are licensed under the [Apache License 2.0](LICENSE), Copyright
2026 Sideways Works UG (haftungsbeschränkt). See [NOTICE](NOTICE),
[AUTHORS](AUTHORS), [CONTRIBUTING](CONTRIBUTING.md) and
[SECURITY](SECURITY.md).
