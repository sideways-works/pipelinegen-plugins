# github-connector

A pipelinegen connector for repositories on GitHub, github.com or GitHub
Enterprise Server. [pipelinegen](https://github.com/sideways-works/pipelinegen)
runs only the test slices a change can break; a connector tells it where
the code lives. This one lists a repository's open pull requests, sets
commit statuses and keeps one comment on each pull request.

```mermaid
flowchart LR
  core["pipelinegen"] -- "call and the responses so far (JSON)" --> conn["github-connector"]
  conn -- "the requests to send, or the answer" --> core
  core -- "each request, with the token of the call's purpose" --> api["GitHub's API"]
  api -- "responses, as files" --> core
```

It sends nothing itself and never sees a token. Each call answers with the
requests it wants sent, as data. pipelinegen checks them, adds the token
for the call's purpose, sends them only to the API host the step names,
retries, and hands back the responses. This goes round by
round until the connector gives its answer.

## Using it

A config with a `github` section uses it, and needs no line of its own
for it: pipelinegen pins a release of it and fetches that release, checked
by its checksum. A `plugins` entry named `github` pins another release:

```yaml
plugins:
  - repository: https://github.com/sideways-works/pipelinegen-plugins
    with:
      - {name: github, kind: connector, path: connector/github, release: {version: v0.4.0, sums: …}}
```

On GitHub Enterprise Server, the step's `PIPELINEGEN_GITHUB_API` names the
API (`https://git.example.com/api/v3`).

## What it answers

| Call | What it does |
|---|---|
| `describe` | the capabilities changes, statuses, comment, draft, labels, ready_since, forks and head_ref; `https://api.github.com` as its API host; `Authorization: Bearer` for the tokens of the read, changes and report purposes; statuses up to 140 characters and comments up to 65536, in Markdown with Mermaid |
| `identify` | a remote on github.com, or on the host of a configured API, by its `owner/name`; a pull request's head is fetched by `refs/pull/{id}/head` |
| `changes` | the open pull requests onto a base, up to ten pages of a hundred, with their head, branch, draft state and labels, narrowed by the run's filters (draft, labels, the head branch's pattern); with `ready_since`, when each was last marked ready, from its timeline, twenty timelines a round |
| `status`, `statuses` | sets a commit status; reads a commit's newest status per context. A commit GitHub doesn't know has none |
| `subject` | a commit's subject line |
| `comment` | keeps one comment on a pull request: the oldest whose body starts with the marker, on a line of its own or alone, is updated when its text differs, one is made when there is none, and the others that start with the marker are deleted. A comment that only quotes the marker further down is someone else's and stays |
| `links` | a commit's, a file's and a pull request's page |

A pull request runs only when its head's repository has the base
repository's id. A fork has another id, and so does a repository deleted
and made again under the same name by someone else; both are reported as
a fork. A pull request whose head repository is gone is reported as
unknown. Neither runs.

## Checking it

```sh
go test ./...
```

runs pipelinegen's conformance kit on the built connector with the fixture
in `connector/testdata/kit`: two pull requests, one of them from a fork, a
status, a commit and an empty comment thread. By hand:
`pipelinegen plugin test <the built executable> --tree connector/testdata/kit`.

`connector/testdata/requests.json` keeps the requests pipelinegen's own
GitHub clients sent before this connector replaced them, and a test holds
the connector to them, request by request.

## Developing

`bazel run //connector/github:test` runs the tests and `bazel run
//connector/github:lint` the lint, with the Go, golangci-lint and Node
the repository pins in `MODULE.bazel`
([CONTRIBUTING](../../CONTRIBUTING.md)). The tests include pipelinegen's
conformance kit: `TestTheKit` builds github-connector and runs it
through the kit with the fixture in `connector/testdata/kit`, using the
pipelinegen that `go.mod` requires as a tool.

By hand, in this directory:

```sh
go build -trimpath -o github-connector ./cmd/github-connector
go tool pipelinegen plugin test ./github-connector --tree connector/testdata/kit
```

`bazel run //connector/github:release` releases it from a tag
`connector/github/vX.Y.Z` on `main`, as
[CONTRIBUTING](../../CONTRIBUTING.md#releases) describes: static
`github-connector-linux-amd64`, `-linux-arm64`, `-darwin-amd64` and
`-darwin-arm64`, `plugin.json` (its own `describe` answer), `LICENSE`,
`NOTICE` and `SHA256SUMS`. Its first release here is `v0.4.0`
([RELEASING](../../RELEASING.md)), and a published release never
changes.

A change to what github-connector answers comes with its docs, so this
README describes github-connector as it is on `main`.
