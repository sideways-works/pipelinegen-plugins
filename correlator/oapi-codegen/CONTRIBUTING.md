# Contributing to oapi-codegen-correlator

Thank you for wanting to help. This page says how to set up a working
copy, what every change has to pass, and how commits are written. What
oapi-codegen-correlator does, how it names the code that serves each
operation, how a run is recorded and what it can't see, is in the
[README](README.md). The protocol it speaks is in pipelinegen's book:
[the plugin protocol](https://github.com/sideways-works/pipelinegen/blob/main/docs/plugins.md)
and [writing a correlator](https://github.com/sideways-works/pipelinegen/blob/main/docs/writing-plugins/correlator.md).

## Reporting a problem

Open an issue on the repository with what you ran, what you expected and
what happened, and the version oapi-codegen-correlator reports (`echo
'{"protocol": 1}' | oapi-codegen-correlator describe`). For an operation
linked to the wrong code, or a request that wasn't counted, the
contract, the oapi-codegen version and the `why` answer for the slice in
question say most of what we need. A security problem goes through the
private route in [SECURITY.md](SECURITY.md), never an issue.

## A working copy

You need Go and golangci-lint at the versions in `.tool-versions`, and
git. openapi-sbom, built from [its
repository](https://github.com/sideways-works/pipelinegen-sbom-openapi)
or taken from a release, runs beside it in the conformance kit.

```sh
git clone https://github.com/sideways-works/pipelinegen-correlator-oapi-codegen.git
cd pipelinegen-correlator-oapi-codegen
go build -trimpath -o oapi-codegen-correlator ./cmd/oapi-codegen-correlator
```

## What every change passes

```sh
GOFLAGS=-trimpath go test ./...
golangci-lint run ./...                  # the repository's .golangci.yml
pipelinegen plugin test ./oapi-codegen-correlator --tree correlate/testdata/repo --sbom <openapi-sbom>
pipelinegen plugin test ./oapi-codegen-correlator --tree correlate/testdata/split --sbom <openapi-sbom>
```

- The fixture in `correlate/testdata/repo` is a module with a contract
  and the code oapi-codegen generated from it, and its `go.mod` requires
  the generator and the generated code's modules. The tests read those
  from the module cache, so fill it once (`go mod download` in
  `correlate/testdata/repo`); after that they use no network.
- The last two lines are pipelinegen's conformance kit. It calls the
  oapi-codegen-correlator you built the way pipelinegen does, on the
  fixture with the recording its `.pipelinegen-kit.json` names, and on
  `correlate/testdata/split`, a contract split into one file per
  context. openapi-sbom beside it lists the units the links name. Take
  `pipelinegen` from [its
  releases](https://github.com/sideways-works/pipelinegen/blob/main/docs/tutorial/install.md).
- A change to what oapi-codegen-correlator links or observes comes with
  its docs: the README describes it as it is on `main`.

## Commits and pull requests

A commit message is one conventional subject line of at most 72
characters, such as `test: the kit records each fixture's slice in two
parts`, with no body and no trailers. The types are `feat`, `fix`,
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

oapi-codegen-correlator is licensed under the [Apache License
2.0](LICENSE). By contributing you agree that your contribution is
licensed the same way.
