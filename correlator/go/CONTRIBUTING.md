# Contributing to go-correlator

Thank you for wanting to help. This page says how to set up a working
copy, what every change has to pass, and how commits are written. What
go-correlator records, how it binds a slice and how a run is recorded is
in the [README](README.md). The protocol it speaks is in pipelinegen's
book:
[the plugin protocol](https://github.com/sideways-works/pipelinegen/blob/main/docs/plugins.md)
and [writing a correlator](https://github.com/sideways-works/pipelinegen/blob/main/docs/writing-plugins/correlator.md).

## Reporting a problem

Open an issue on the repository with what you ran, what you expected and
what happened, and the version go-correlator reports (`echo '{"protocol":
1}' | go-correlator describe`). For a slice that was bound too narrowly or
too widely, the smallest tree that shows it and the `why` answer for the
slice in question say most of what we need. A security problem goes
through the private route in [SECURITY.md](SECURITY.md), never an issue.

## A working copy

You need Go and golangci-lint at the versions in `.tool-versions`, and
git. go-sbom, built from
[its repository](https://github.com/sideways-works/pipelinegen-sbom-go)
or taken from a release, runs beside it in the conformance kit.

```sh
git clone https://github.com/sideways-works/pipelinegen-correlator-go.git
cd pipelinegen-correlator-go
go build -trimpath -o go-correlator ./cmd/go-correlator
```

## What every change passes

```sh
GOFLAGS=-trimpath go test ./...
golangci-lint run ./...                  # the repository's .golangci.yml
(cd cover && GOFLAGS=-trimpath go test ./... && golangci-lint run ./...)
pipelinegen plugin test ./go-correlator --tree graph/testdata --sbom <go-sbom>
```

- `cover` is a module of its own, which coverage builds of a repository
  link, so `./...` stops at it and it is tested and linted on its own.
  It keeps to the standard library.
- The last line is pipelinegen's conformance kit. It calls the
  go-correlator you built the way pipelinegen does, on the fixture tree
  in `graph/testdata`, whose `.pipelinegen-kit.json` names the slices to
  bind and the recording in `coverage/` that `observe` reads. With go-sbom
  beside it as `--sbom`, the kit also checks that each link changes only
  when one of its ends does. Take `pipelinegen` from
  [its releases](https://github.com/sideways-works/pipelinegen/blob/main/docs/tutorial/install.md).
- Tests type-check fixture trees through the go command, so the go on
  your `PATH` must be at least the version in `go.mod`. They use no
  network: a test that needs modules serves them from a directory.
- A change to what go-correlator binds comes with its docs: the README
  describes go-correlator as it is on `main`, and a narrowing says there
  what it can miss.

## Commits and pull requests

A commit message is one conventional subject line of at most 72
characters, such as `fix: a nested module read twice no longer stops the
load`, with no body and no trailers. The types are `feat`, `fix`, `perf`,
`refactor`, `test`, `docs`, `build` and `ci`. Keep commits small and each
one passing.

Open a pull request against `main`.

## Releases

Releases are cut by the maintainers with pipelinegen's `ci/release.sh`,
as its [release guide](https://github.com/sideways-works/pipelinegen/blob/main/docs/releases.md)
describes, from a tag `vX.Y.Z` on `main`. The `cover` module is released
with it: the same commit also gets the tag `cover/vX.Y.Z`. A candidate is
tagged `vX.Y.Z-rc.N` (and `cover/vX.Y.Z-rc.N`) and published as a
prerelease that is never marked the latest. A published release never
changes; a wrong one gets a new version.

## Licence

go-correlator is licensed under the [Apache License 2.0](LICENSE), and so
is the `cover` module, which carries its own copy of the licence because
it is fetched apart from the rest. By contributing you agree that your
contribution is licensed the same way.
