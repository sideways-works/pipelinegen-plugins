# Contributing to shell-driver

Thank you for wanting to help. This page says how to set up a working
copy, what every change has to pass, and how commits are written. What a
driver is and the protocol it speaks are in pipelinegen's book: [the
concept](https://github.com/sideways-works/pipelinegen/blob/main/docs/concepts/drivers.md)
and [writing a driver](https://github.com/sideways-works/pipelinegen/blob/main/docs/writing-plugins/driver.md).

## Reporting a problem

Open an issue on the repository with the step's settings, what you ran,
what you expected and what happened, and the driver's version
(`shell-driver describe`). A security problem goes through the private
route in [SECURITY.md](SECURITY.md), never an issue.

## A working copy

You need Go (the version in `go.mod`), git, and `sh` on your `PATH`; the
tests use `bash` too.

```sh
git clone https://github.com/sideways-works/pipelinegen-driver-shell.git
cd pipelinegen-driver-shell
go build -trimpath ./cmd/shell-driver
```

## What every change passes

```sh
GOFLAGS=-trimpath go test ./...
golangci-lint run ./...           # version 2.14, the repository's .golangci.yml
```

The tests include pipelinegen's conformance kit (`go tool pipelinegen
plugin test`) on the fixture in `driver/testdata/sh`, which runs every
verb the driver declares. A change to what the driver does comes with its
test and its paragraph in the README.

## Commits and pull requests

A commit message is one conventional subject line of at most 72
characters, such as `fix: a bundle's slices keep their own results`, with
no body and no trailers. The types are `feat`, `fix`, `perf`, `refactor`,
`test`, `docs`, `build` and `ci`. Keep commits small and each one passing.

Open a pull request against `main`.

## Licence

shell-driver is licensed under the [Apache License 2.0](LICENSE). By
contributing you agree that your contribution is licensed the same way.
