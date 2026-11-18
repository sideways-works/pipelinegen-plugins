# Contributing to go-driver

Thank you for wanting to help. This page says how to set up a working
copy, what every change has to pass, and how commits are written. What
go-driver runs, and how it bundles and records, is in the
[README](README.md). The protocol it speaks is in pipelinegen's book:
[drivers](https://github.com/sideways-works/pipelinegen/blob/main/docs/concepts/drivers.md)
and [writing a driver](https://github.com/sideways-works/pipelinegen/blob/main/docs/writing-plugins/driver.md).

## Reporting a problem

Open an issue on the repository with what you ran, what you expected and
what happened, and the version go-driver reports (`echo '{"protocol": 1}'
| go-driver describe`). For a slice that got the wrong result or the
wrong recording, the step's settings, whether it was bundled or recording,
and the go test output the driver logged say most of what we need. A
security problem goes through the private route in
[SECURITY.md](SECURITY.md), never an issue.

## A working copy

You need Go and golangci-lint at the versions in `.tool-versions`, and
git.

```sh
git clone https://github.com/sideways-works/pipelinegen-driver-go.git
cd pipelinegen-driver-go
go build -trimpath -o go-driver ./cmd/go-driver
```

## What every change passes

```sh
GOFLAGS=-trimpath go test ./...
golangci-lint run ./...                  # the repository's .golangci.yml
```

- The tests include pipelinegen's conformance kit: `TestTheKit` builds
  the driver and runs `go tool pipelinegen plugin test` on it with the
  fixture module in `driver/testdata/mod`, with the pipelinegen that
  `go.mod` requires as a tool. The kit runs every slice of the fixture one
  by one and bundled, recording and not, compares the results and the
  recordings, and checks the marks of a recorded stack suite's bundle. By
  hand: `pipelinegen plugin test ./go-driver --tree driver/testdata/mod`.
- Tests run the go command on the fixture module, whose `kid` package
  starts a child process, so the go on your `PATH` must be at least the
  version in `go.mod`.
- A change to what go-driver runs comes with its docs: the README
  describes go-driver as it is on `main`.

## Commits and pull requests

A commit message is one conventional subject line of at most 72
characters, such as `fix: a bundle's tests see go test's environment;
bundles keep apart`, with no body and no trailers. The types are `feat`, `fix`, `perf`,
`refactor`, `test`, `docs`, `build`, `ci` and `chore`. Keep commits small
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

go-driver is licensed under the [Apache License 2.0](LICENSE). By
contributing you agree that your contribution is licensed the same way.
