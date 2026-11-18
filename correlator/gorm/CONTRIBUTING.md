# Contributing to gorm-correlator

Thank you for wanting to help. This page says how to set up a working
copy, what every change has to pass, and how commits are written. What
gorm-correlator does, which tables GORM code reaches, how it reads a
statement and what it can't see, is in the [README](README.md). The
protocol it speaks is in pipelinegen's book:
[the plugin protocol](https://github.com/sideways-works/pipelinegen/blob/main/docs/plugins.md)
and [writing a correlator](https://github.com/sideways-works/pipelinegen/blob/main/docs/writing-plugins/correlator.md).

## Reporting a problem

Open an issue on the repository with what you ran, what you expected and
what happened, and the version gorm-correlator reports (`echo
'{"protocol": 1}' | gorm-correlator describe`). For a link that is
missing or wrong, the smallest model and query that show it and the
`why` answer for the slice in question say most of what we need. A
security problem goes through the private route in
[SECURITY.md](SECURITY.md), never an issue.

## A working copy

You need Go and golangci-lint at the versions in `.tool-versions`, and
git.

```sh
git clone https://github.com/sideways-works/pipelinegen-correlator-gorm.git
cd pipelinegen-correlator-gorm
go build -trimpath -o gorm-correlator ./cmd/gorm-correlator
```

## What every change passes

```sh
GOFLAGS=-trimpath go test ./...
golangci-lint run ./...                  # the repository's .golangci.yml
kit=$(mktemp -d)
(cd internal/analysis/testdata/app && GOMODCACHE=$kit/gomodcache go mod download)
pipelinegen plugin test ./gorm-correlator --tree internal/analysis/testdata/app --cache "$kit" --sbom <go-sbom>
```

- The tests type-check the small modules under
  `internal/analysis/testdata`, which require GORM, so fill the module
  cache once (`go mod download` in `internal/analysis/testdata/app`);
  after that they use no network.
- The last lines are pipelinegen's conformance kit. It calls the
  gorm-correlator you built the way pipelinegen does, on the fixture
  module, with a module cache of its own that you fill first. With
  go-sbom beside it as `--sbom` (built from [its
  repository](https://github.com/sideways-works/pipelinegen-sbom-go) or
  taken from a release), the kit also checks that each link changes only
  when one of its ends does; pg-sbom does the same. Take `pipelinegen`
  from [its
  releases](https://github.com/sideways-works/pipelinegen/blob/main/docs/tutorial/install.md).
- A change to what gorm-correlator links comes with its docs: the README
  describes gorm-correlator as it is on `main`.

## Commits and pull requests

A commit message is one conventional subject line of at most 72
characters, such as `fix: a path in a replaced directory answers the
replacing module too`, with no body and no trailers. The types are
`feat`, `fix`, `perf`, `refactor`, `test`, `docs`, `build`, `ci` and
`chore`. Keep commits small and each one passing.

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

gorm-correlator is licensed under the [Apache License 2.0](LICENSE). By
contributing you agree that your contribution is licensed the same way.
