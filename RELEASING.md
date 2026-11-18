# Releasing the plugins

Each plugin is released on its own, from a tag `<kind>/<name>/vX.Y.Z` on
`main`, by its `:release` target ([CONTRIBUTING](CONTRIBUTING.md#releases)
has its forms). Some plugins require others' modules, so the plugins
are released in a set order. Below are that order and the first releases
from this repository.

## Before the first release

`:release` runs pipelinegen's release command at the version
`tools/go.mod` requires, v0.4.0-rc.1 or later: the first release that
releases a module in a directory of a repository.

runner/concourse requires pipelinegen v0.4.0-rc.1, whose conformance kit
runs the runner's own step program; v0.3.1's kit looked for the tool's
calls in task scripts, which the runner no longer writes. Every other
plugin requires v0.3.1, since a plugin doesn't require a prerelease, and
moves to the first v0.4.0 of pipelinegen when it is out.

## The first releases: v0.4.0

Every plugin's first release here is `v0.4.0`
(`driver/go/v0.4.0`, `correlator/go/v0.4.0` with
`correlator/go/cover/v0.4.0`, `correlator/locale/v0.4.0` with
`correlator/locale/gorecord/v0.4.0`), whatever its last version was in
its own repository. The commits those older releases were cut from hold
`go.mod` files with the old module paths, so none of them is tagged here.

## What requires what

| Plugin | Requires, besides pipelinegen |
|---|---|
| correlator/go | sbom/go |
| correlator/gorm | sbom/go, sbom/pg |
| correlator/locale | sbom/locale |
| correlator/oapi-codegen | sbom/go, sbom/openapi |
| correlator/sql | sbom/pg |
| correlator/react | sbom/ts, from the npm workspace at the same commit |

The Go requirements still name the sbom plugins' modules by the paths
they had before they moved here, at v0.2.0. Every other plugin requires
none of the others.

## The order

1. The sbom plugins: sbom/go, sbom/pg, sbom/locale, sbom/openapi,
   sbom/npm and sbom/ts, each `v0.4.0`. With them, every plugin that
   requires no other: connector/git, connector/github, driver/bazel,
   driver/go, driver/playwright, driver/shell, runner/concourse and
   correlator/react, which bundles ts-sbom from the
   same commit.
2. In one commit, move each correlator that requires an sbom's module to
   `github.com/sideways-works/pipelinegen-plugins/sbom/<name>` `v0.4.0`:
   its imports, its `go.mod` and `go.sum`, and its `NOTICE`
   (`bazel run //correlator/<name>:release -- notice`, since the NOTICE
   names the repository the code comes from). `bazel run //:test` and
   `bazel run //:lint` pass on it.
3. The correlators that require them: correlator/go, correlator/gorm,
   correlator/locale, correlator/oapi-codegen and correlator/sql, each
   `v0.4.0`, from that commit or a later one.
4. Move the pins: this repository's `pipelinegen.yaml` (its runner) and
   every adopter's, to the grouped form the README shows. Only once no
   pin, `go.mod` or lockfile names an old repository is it deleted: a
   deleted repository's name stops redirecting.

For each release, run the plugin's checks, run `notice` and commit what
changed, then run `bazel run //<kind>/<name>:release -- patch v0.4.0 <out>`,
which tags `main`'s head (and a nested module's tag), builds, publishes
and checks the release by download. `bazel run //<kind>/<name>:release
-- check v0.4.0` checks a published release against a fresh build of its
tag at any time.
