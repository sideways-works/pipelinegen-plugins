# example-forge

A minimal forge plugin for
[pipelinegen](https://github.com/sideways-works/pipelinegen): it claims
the remotes on its API's host, lists a repository's open changes with
one request to that API, and names the host's pages of a commit, a file
and a change. It answers `describe`, `identify`, `changes` and `links`;
the core sends its requests, with the token in the header `describe`
names.

It passes pipelinegen's conformance kit on its fixture,
`testdata/kit`. Start a new forge from it: the root
[CONTRIBUTING](../../CONTRIBUTING.md#adding-a-plugin) says how to make a plugin of it.

```sh
bazel run //templates/forge:test     # its tests, the kit among them
bazel run //templates/forge:lint
bazel run //:templates            # every template built and run through the kit
```

The kit by hand, in this directory:

```sh
go build -trimpath -o example-forge ./cmd/example-forge
go tool pipelinegen plugin test ./example-forge --tree testdata/kit
```
