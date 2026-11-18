# Contributing to bazel-driver

Thank you for wanting to help. This page says how to set up a working
copy, what every change has to pass, and how commits are written. What
bazel-driver runs, how it reads results and how it holds a recorded stack
suite at each test, is in the [README](README.md). The protocol it speaks
is in pipelinegen's book:
[drivers](https://github.com/sideways-works/pipelinegen/blob/main/docs/concepts/drivers.md)
and [writing a driver](https://github.com/sideways-works/pipelinegen/blob/main/docs/writing-plugins/driver.md).

## Reporting a problem

Open an issue on the repository with what you ran, what you expected and
what happened, and the version bazel-driver reports (`echo '{"protocol":
1}' | bazel-driver describe`). For a target that got the wrong result or
no result, the step's settings, the Bazel version, and the build event
file of the invocation say most of what we need. A security problem goes
through the private route in [SECURITY.md](SECURITY.md), never an issue.

## A working copy

You need Go and golangci-lint at the versions in `.tool-versions`, and
git. The tests need no Bazel.

```sh
git clone https://github.com/sideways-works/pipelinegen-driver-bazel.git
cd pipelinegen-driver-bazel
go build -trimpath -o bazel-driver ./cmd/bazel-driver
```

## What every change passes

```sh
GOFLAGS=-trimpath go test ./...
golangci-lint run ./...                  # the repository's .golangci.yml
```

- Tests run the driver against a fake Bazel that records the invocations
  it is given, and read results from the build event and JUnit files in
  `internal/bep/testdata` and `internal/junit/testdata`.
- pipelinegen's conformance kit, `pipelinegen plugin test`, runs a driver
  on a fixture tree whose `.pipelinegen-kit.json` has a `driver` section.
  This repository has no such fixture yet, and the kit fails without one,
  so it is not among these checks.
- A change to what bazel-driver runs comes with its docs: the README
  describes bazel-driver as it is on `main`.

## Commits and pull requests

A commit message is one conventional subject line of at most 72
characters, such as `fix: a target's status is its test summary's, every
shard kept`, with no body and no trailers. The types are `feat`, `fix`,
`perf`, `refactor`, `test`, `docs`, `build`, `ci` and `chore`. Keep
commits small and each one passing.

Open a pull request against `main`.

## Releases

Releases are cut by the maintainers with pipelinegen's `ci/release.sh`,
as its [release guide](https://github.com/sideways-works/pipelinegen/blob/main/docs/releases.md)
describes, from a tag `vX.Y.Z` on `main`. A candidate is tagged
`vX.Y.Z-rc.N` and published as a prerelease that is never marked the
latest. A published release never changes; a wrong one gets a new
version.

## Licence

bazel-driver is licensed under the [Apache License 2.0](LICENSE). By
contributing you agree that your contribution is licensed the same way.
