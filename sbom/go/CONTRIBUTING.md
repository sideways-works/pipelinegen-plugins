# Contributing to go-sbom

Thank you for wanting to help. This page says how to set up a working
copy, what every change has to pass, and how commits are written. What
go-sbom lists, and how its ids and digests are made, is in the
[README](README.md). The protocol it speaks is in pipelinegen's book:
[the plugin protocol](https://github.com/sideways-works/pipelinegen/blob/main/docs/plugins.md)
and [writing an sbom plugin](https://github.com/sideways-works/pipelinegen/blob/main/docs/writing-plugins/sbom.md).

## Reporting a problem

Open an issue on the repository with what you ran, what you expected and
what happened, and the version go-sbom reports (`echo '{"protocol": 1}' |
go-sbom describe`). For a digest that changed when it shouldn't have, or
stayed when it should have changed, the smallest tree that shows it and
the `units` request you sent say most of what we need. A security problem
goes through the private route in [SECURITY.md](SECURITY.md), never an
issue.

## A working copy

You need Go and golangci-lint at the versions in `.tool-versions`, and
git.

```sh
git clone https://github.com/sideways-works/pipelinegen-sbom-go.git
cd pipelinegen-sbom-go
go build -trimpath -o go-sbom ./cmd/go-sbom
```

## What every change passes

```sh
GOFLAGS=-trimpath go test ./...
golangci-lint run ./...                          # the repository's .golangci.yml
pipelinegen plugin test ./go-sbom --tree testdata/kit
```

- The last line is pipelinegen's conformance kit. It calls the go-sbom
  you built the way pipelinegen does, on the fixture tree in
  `testdata/kit`, and checks every answer against the protocol. Take
  `pipelinegen` from
  [its releases](https://github.com/sideways-works/pipelinegen/blob/main/docs/tutorial/install.md).
- Tests use no network and no module cache of yours: their trees and
  caches are fixtures in temporary directories.
- The ids in `goid` are what other plugins import to name a declaration.
  Their format changes only with a new major version of this module.
- A change to what go-sbom answers comes with its docs: the README
  describes go-sbom as it is on `main`.

## Commits and pull requests

A commit message is one conventional subject line of at most 72
characters, such as `fix: a package that doesn't parse lists its files and
can't tell`, with no body and no trailers. The types are `feat`, `fix`,
`perf`, `refactor`, `test`, `docs`, `build` and `ci`. Keep commits small
and each one passing.

Open a pull request against `main`.

## Releases

Releases are cut by the maintainers with pipelinegen's `ci/release.sh`,
as its [release guide](https://github.com/sideways-works/pipelinegen/blob/main/docs/releases.md)
describes, from a tag `vX.Y.Z` on `main`. A candidate is tagged
`vX.Y.Z-rc.N` and published as a prerelease that is never marked the
latest. A published release never changes; a wrong one gets a new
version.

## Licence

go-sbom is licensed under the [Apache License 2.0](LICENSE). By
contributing you agree that your contribution is licensed the same way.
