# Contributing to npm-sbom

Thank you for wanting to help. This page says how to set up a working
copy, what every change has to pass, and how commits are written. What
npm-sbom does, which lockfiles it reads and how each package's unit and
digest are made, is in the [README](README.md). The protocol it speaks
is in pipelinegen's book:
[the plugin protocol](https://github.com/sideways-works/pipelinegen/blob/main/docs/plugins.md)
and [writing an sbom plugin](https://github.com/sideways-works/pipelinegen/blob/main/docs/writing-plugins/sbom.md).

## Reporting a problem

Open an issue on the repository with what you ran, what you expected and
what happened, and the version npm-sbom reports (`echo '{"protocol": 1}'
| npm-sbom describe`). For a package that is missing or has the wrong
digest, the smallest lockfile that shows it and the `units` request you
sent say most of what we need. A security problem goes through the
private route in [SECURITY.md](SECURITY.md), never an issue.

## A working copy

You need Go and golangci-lint at the versions in `.tool-versions`, and
git.

```sh
git clone https://github.com/sideways-works/pipelinegen-sbom-npm.git
cd pipelinegen-sbom-npm
go build -trimpath -o npm-sbom ./cmd/npm-sbom
```

## What every change passes

```sh
GOFLAGS=-trimpath go test ./...
golangci-lint run ./...                  # the repository's .golangci.yml
pipelinegen plugin test ./npm-sbom --tree npm/testdata/workspace
pipelinegen plugin test ./npm-sbom --tree npm/testdata/pnpm
```

- Tests read the fixture workspaces in `npm/testdata` and use no
  network.
- The last two lines are pipelinegen's conformance kit. It calls the
  npm-sbom you built the way pipelinegen does, on an npm workspace and
  on a pnpm one. Take `pipelinegen` from [its
  releases](https://github.com/sideways-works/pipelinegen/blob/main/docs/tutorial/install.md).
- A change to what npm-sbom lists comes with its docs: the README
  describes npm-sbom as it is on `main`.

## Commits and pull requests

A commit message is one conventional subject line of at most 72
characters, such as `feat: read pnpm lockfiles of versions 6.0 and 9.0`,
with no body and no trailers. The types are `feat`, `fix`, `perf`,
`refactor`, `test`, `docs`, `build`, `ci` and `chore`. Keep commits
small and each one passing.

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

npm-sbom is licensed under the [Apache License 2.0](LICENSE). By
contributing you agree that your contribution is licensed the same way.
