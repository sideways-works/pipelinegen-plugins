# Contributing to concourse-runner

Thank you for wanting to help. This page says how to set up a working
copy, what every change has to pass, and how commits are written. What
pipeline concourse-runner writes, and why each part of it is the way it
is, is in the [README](README.md). The protocol it speaks is in
pipelinegen's book:
[the plugin protocol](https://github.com/sideways-works/pipelinegen/blob/main/docs/plugins.md)
and [writing a runner](https://github.com/sideways-works/pipelinegen/blob/main/docs/writing-plugins/runner.md).

## Reporting a problem

Open an issue on the repository with what you ran, what you expected and
what happened, and the version concourse-runner reports (`echo
'{"protocol": 1}' | concourse-runner describe`). For a pipeline that
Concourse refuses or that runs the wrong steps, the config's `runs`,
`suites` and `concourse` block and the output of `fly validate-pipeline
--strict` say most of what we need. A security problem goes through the
private route in [SECURITY.md](SECURITY.md), never an issue.

## A working copy

You need Go and golangci-lint at the versions in `.tool-versions`, git,
and a POSIX shell.

```sh
git clone https://github.com/sideways-works/pipelinegen-runner-concourse.git
cd pipelinegen-runner-concourse
go build -trimpath -o concourse-runner ./cmd/concourse-runner
```

## What every change passes

```sh
GOFLAGS=-trimpath go test ./...
golangci-lint run ./...                  # the repository's .golangci.yml
go tool pipelinegen plugin test ./concourse-runner
```

- The last line is pipelinegen's conformance kit, run with the pipelinegen
  that `go.mod` requires as a tool. It calls the concourse-runner you
  built the way pipelinegen does and checks that the pipeline it writes
  calls only commands and flags that pipelinegen has. A runner needs no
  fixture tree.
- The pipeline's steps call pipelinegen's commands, so the runner and the
  tool move together: a change that makes a step call something new comes
  with the `go.mod` bump to the pipelinegen that has it, and the kit run
  with an older pipelinegen fails on it.
- Tests run each step of `concourse-runner step` against a stand-in for
  pipelinegen, the bootstrap with `sh` against a stand-in for the runner,
  and compare whole pipelines with the files in
  `internal/concourse/testdata`. With `fly` on your `PATH`, they also
  check every pipeline with `fly validate-pipeline --strict`; without it
  those checks are skipped, so run them with `fly` before a pull request.
- No step prints a secret: tests check the generated pipeline and the
  bootstrap for the patterns that would leak the token, and run every
  step to check none passes it on; a change keeps them passing rather
  than loosening them.
- A change to the pipeline comes with its docs: the README describes the
  pipeline as concourse-runner writes it on `main`.

## Commits and pull requests

A commit message is one conventional subject line of at most 72
characters, such as `fix: a suite's end that could not be recorded fails
its job`, with no body and no trailers. The types are `feat`, `fix`,
`perf`, `refactor`, `test`, `docs`, `build`, `ci` and `chore`. Keep
commits small and each one passing.

Open a pull request against `main`.

## Releases

Releases are cut by the maintainers with pipelinegen's `ci/release.sh`,
as its [release guide](https://github.com/sideways-works/pipelinegen/blob/main/docs/releases.md)
describes, from a tag `vX.Y.Z` on `main`. A candidate is tagged
`vX.Y.Z-rc.N` and published as a prerelease that is never marked the
latest. A published release never changes; a wrong one gets a new
version. A change to what the pipeline's steps call ships together with
the pipelinegen release that has those commands: a repository moves both
pins in one commit.

## Licence

concourse-runner is licensed under the [Apache License 2.0](LICENSE). By
contributing you agree that your contribution is licensed the same way.
