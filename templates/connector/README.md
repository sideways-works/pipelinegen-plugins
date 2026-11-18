# example-connector

A minimal connector plugin for
[pipelinegen](https://github.com/sideways-works/pipelinegen): it claims
the remotes on its API's host, lists a repository's open changes with
one request to that API, and names the host's pages of a commit, a file
and a change. It answers `describe`, `identify`, `changes` and `links`;
the core sends its requests, with the token in the header `describe`
names.

It passes pipelinegen's conformance kit on its fixture,
`testdata/kit`. Start a new connector from it: the root
[README](../../README.md#adding-a-plugin) says how to make a plugin of it.

```sh
bazel run //templates/connector:test     # its tests, the kit among them
bazel run //templates/connector:lint
bazel run //:templates            # every template built and run through the kit
```

The kit by hand, in this directory:

```sh
go build -trimpath -o example-connector ./cmd/example-connector
go tool pipelinegen plugin test ./example-connector --tree testdata/kit
```
