# Contributing to sql-correlator

Thank you for wanting to help. This page says how to set up a working
copy, what every change has to pass, and how commits are written. What
sql-correlator does, which log it reads, how a run is recorded and what
it can't see, is in the [README](README.md). The protocol it speaks is
in pipelinegen's book:
[the plugin protocol](https://github.com/sideways-works/pipelinegen/blob/main/docs/plugins.md)
and [writing a correlator](https://github.com/sideways-works/pipelinegen/blob/main/docs/writing-plugins/correlator.md).

## Reporting a problem

Open an issue on the repository with what you ran, what you expected and
what happened, and the version sql-correlator reports (`echo
'{"protocol": 1}' | sql-correlator describe`). For a test whose touched
tables are missing or wrong, the PostgreSQL logging settings, a few
lines of the statement log and the `why` answer for the slice in
question say most of what we need. A security problem goes through the
private route in [SECURITY.md](SECURITY.md), never an issue.

## A working copy

You need Go and golangci-lint at the versions in `.tool-versions`, and
git.

```sh
git clone https://github.com/sideways-works/pipelinegen-correlator-sql.git
cd pipelinegen-correlator-sql
go build -trimpath -o sql-correlator ./cmd/sql-correlator
```

## What every change passes

```sh
GOFLAGS=-trimpath go test ./...
golangci-lint run ./...                  # the repository's .golangci.yml
pipelinegen plugin test ./sql-correlator --tree testdata
```

- Tests read the small statement logs in `testdata`, need no database
  and use no network.
- The last line is pipelinegen's conformance kit. sql-correlator reads
  no file of the tree, but the kit needs one, and
  `testdata/.pipelinegen-kit.json` names a recording in it that the kit
  observes. Take `pipelinegen` from [its
  releases](https://github.com/sideways-works/pipelinegen/blob/main/docs/tutorial/install.md).
- Ids come from pg-sbom's `pgid` package, so both plugins name a
  database object the same way.
- A change to what sql-correlator observes comes with its docs: the
  README describes sql-correlator as it is on `main`.

## Commits and pull requests

A commit message is one conventional subject line of at most 72
characters, such as `fix: an unreadable log line is can't tell, not a
failed collect`, with no body and no trailers. The types are `feat`,
`fix`, `perf`, `refactor`, `test`, `docs`, `build`, `ci` and `chore`.
Keep commits small and each one passing.

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

sql-correlator is licensed under the [Apache License 2.0](LICENSE). By
contributing you agree that your contribution is licensed the same way.
