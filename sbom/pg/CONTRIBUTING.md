# Contributing to pg-sbom

Thank you for wanting to help. This page says how to set up a working
copy, what every change has to pass, and how commits are written. What
pg-sbom does, how it reads migrations, what it lists and how each unit's
id and digest are made, is in the [README](README.md). The protocol it
speaks is in pipelinegen's book:
[the plugin protocol](https://github.com/sideways-works/pipelinegen/blob/main/docs/plugins.md)
and [writing an sbom plugin](https://github.com/sideways-works/pipelinegen/blob/main/docs/writing-plugins/sbom.md).

## Reporting a problem

Open an issue on the repository with what you ran, what you expected and
what happened, and the version pg-sbom reports (`echo '{"protocol": 1}'
| pg-sbom describe`). For an object that is missing, or a digest that
changed when it shouldn't have, the smallest migration tree that shows
it and the `units` request you sent say most of what we need. A security
problem goes through the private route in [SECURITY.md](SECURITY.md),
never an issue.

## A working copy

You need Go and golangci-lint at the versions in `.tool-versions`, and
git. No C toolchain and no database: PostgreSQL's parser comes compiled
to WebAssembly.

```sh
git clone https://github.com/sideways-works/pipelinegen-sbom-pg.git
cd pipelinegen-sbom-pg
go build -trimpath -o pg-sbom ./cmd/pg-sbom
```

## What every change passes

```sh
GOFLAGS=-trimpath go test ./...
golangci-lint run ./...                  # the repository's .golangci.yml
pipelinegen plugin test ./pg-sbom --tree testdata/notes
```

- Tests read the small migration trees in `testdata`, need no database
  and use no network.
- The last line is pipelinegen's conformance kit. It calls the pg-sbom
  you built the way pipelinegen does, on the fixture tree. Take
  `pipelinegen` from [its
  releases](https://github.com/sideways-works/pipelinegen/blob/main/docs/tutorial/install.md).
- The ids made by the `pgid` package are what sql-correlator and
  gorm-correlator name database objects by, so a change to an id's form
  is a change to those plugins too.
- `sqlref/builtins.txt` and `model/extensions.txt` are generated; the
  README says how to refresh them.
- A change to what pg-sbom lists comes with its docs: the README
  describes pg-sbom as it is on `main`.

## Commits and pull requests

A commit message is one conventional subject line of at most 72
characters, such as `fix: the unknown-layout warning no longer points at
the config`, with no body and no trailers. The types are `feat`, `fix`,
`perf`, `refactor`, `test`, `docs`, `build`, `ci` and `chore`. Keep
commits small and each one passing.

Open a pull request against `main`.

## Releases

Releases are cut by the maintainers with pipelinegen's `ci/release.sh`,
as its [release
guide](https://github.com/sideways-works/pipelinegen/blob/main/docs/releases.md)
describes, from a tag `vX.Y.Z` on `main`. A candidate is tagged
`vX.Y.Z-rc.N` and published as a prerelease that is never marked the
latest. A published release never changes; a wrong one gets a new
version.

## Licence

pg-sbom is licensed under the [Apache License 2.0](LICENSE). By
contributing you agree that your contribution is licensed the same way.
