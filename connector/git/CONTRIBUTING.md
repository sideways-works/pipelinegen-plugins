# Contributing to git-connector

Thank you for wanting to help. This page says how to set up a working
copy, what every change has to pass, and how commits are written. What
git-connector does is in the [README](README.md). The protocol it speaks is in
pipelinegen's book:
[connectors](https://github.com/sideways-works/pipelinegen/blob/main/docs/concepts/connectors.md)
and [writing a connector](https://github.com/sideways-works/pipelinegen/blob/main/docs/writing-plugins/connector.md).

## Reporting a problem

Open an issue on the repository with what you ran, what you expected and
what happened, and the version git-connector reports (`echo '{"protocol": 1}'
| git-connector describe`). A security problem goes through the private route in
[SECURITY.md](SECURITY.md), never an issue.

## A working copy

You need Go and golangci-lint at the versions in `.tool-versions`, and
git.

```sh
git clone https://github.com/sideways-works/pipelinegen-connector-git.git
cd pipelinegen-connector-git
go build -trimpath -o git-connector ./cmd/git-connector
```

## What every change passes

```sh
GOFLAGS=-trimpath go test ./...
golangci-lint run ./...                  # the repository's .golangci.yml
```

- The tests include pipelinegen's conformance kit: `TestTheKit` builds
  the connector and runs `go tool pipelinegen plugin test` on it with the
  fixture in `connector/testdata/kit`, with the pipelinegen that `go.mod` requires as
  a tool. By hand:
  `pipelinegen plugin test ./git-connector --tree connector/testdata/kit`.
- A change to what git-connector answers comes with its docs: the README
  describes git-connector as it is on `main`.

## Commits and pull requests

A commit message is one conventional subject line of at most 72
characters, with no body and no trailers. The types are `feat`, `fix`,
`perf`, `refactor`, `test`, `docs`, `build`, `ci` and `chore`. Keep
commits small and each one passing.

Open a pull request against `main`.

## Releases

Releases are cut by the maintainers with pipelinegen's `ci/release`, as
its [release guide](https://github.com/sideways-works/pipelinegen/blob/main/docs/releases.md)
describes, from a tag `vX.Y.Z` on `main`. A candidate is tagged
`vX.Y.Z-rc.N` and published as a prerelease that is never marked the
latest. A published release never changes; a wrong one gets a new
version. pipelinegen pins a release of git-connector with its checksums; a new
release reaches repositories when pipelinegen pins it, or when a config
pins it under `plugins`.

## Licence

git-connector is licensed under the [Apache License 2.0](LICENSE). By
contributing you agree that your contribution is licensed the same way.
