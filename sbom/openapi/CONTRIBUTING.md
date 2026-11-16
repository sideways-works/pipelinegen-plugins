# Contributing to openapi-sbom

Thank you for wanting to help. This page says how to set up a working
copy, what every change has to pass, and how commits are written. What
openapi-sbom does, what it lists from a contract and how each unit's id
and digest are made, is in the [README](README.md). The protocol it
speaks is in pipelinegen's book:
[the plugin protocol](https://github.com/sideways-works/pipelinegen/blob/main/docs/plugins.md)
and [writing an sbom plugin](https://github.com/sideways-works/pipelinegen/blob/main/docs/writing-plugins/sbom.md).

## Reporting a problem

Open an issue on the repository with what you ran, what you expected and
what happened, and the version openapi-sbom reports (`echo '{"protocol":
1}' | openapi-sbom describe`). For a digest that changed when it
shouldn't have, or stayed when it should have changed, the smallest
contract that shows it and the `units` request you sent say most of what
we need. A security problem goes through the private route in
[SECURITY.md](SECURITY.md), never an issue.

## A working copy

You need Go and golangci-lint at the versions in `.tool-versions`, and
git.

```sh
git clone https://github.com/sideways-works/pipelinegen-sbom-openapi.git
cd pipelinegen-sbom-openapi
go build -trimpath -o openapi-sbom ./cmd/openapi-sbom
```

## What every change passes

```sh
GOFLAGS=-trimpath go test ./...
golangci-lint run ./...                  # the repository's .golangci.yml
pipelinegen plugin test ./openapi-sbom --tree openapi/testdata/notes
```

- Tests read the fixture documents in `openapi/testdata` and use no
  network.
- The last line is pipelinegen's conformance kit. It calls the
  openapi-sbom you built the way pipelinegen does, on the fixture
  contract. Take `pipelinegen` from [its
  releases](https://github.com/sideways-works/pipelinegen/blob/main/docs/tutorial/install.md).
- oapi-codegen-correlator names the units openapi-sbom lists, so a
  change to an id's form is a change to both plugins.
- A change to what openapi-sbom lists comes with its docs: the README
  describes openapi-sbom as it is on `main`.

## Commits and pull requests

A commit message is one conventional subject line of at most 72
characters, such as `fix: the document call is per set, read along with
its path items`, with no body and no trailers. The types are `feat`,
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

openapi-sbom is licensed under the [Apache License 2.0](LICENSE). By
contributing you agree that your contribution is licensed the same way.
