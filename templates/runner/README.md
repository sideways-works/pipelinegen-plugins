# example-runner

A minimal runner plugin for
[pipelinegen](https://github.com/sideways-works/pipelinegen): it writes a
Concourse pipeline for a config's branch and timer runs, and says in a
notice which runs it leaves out (pull requests, continuous runs, a
connector's changes).

Each run gets a planning job. Each of its suites gets a job that takes the
tree and the batches from the plan, runs the suite's prepare task, every
batch at once and the whole-suite checks the plan selected, all from the
repository's task files, and then reports each batch, each check and the
suite. Another job makes the pipeline again when the config changes, and
sets it when the config says the pipeline sets itself. Every step runs on
the workers `placement.default` names in the runner's config. Each job
first fetches pipelinegen, bazelisk and the plugins, each pinned by its
public release's checksum or built from its commit.

It passes pipelinegen's conformance kit with the placement in
`testdata/kit/config.json`. Start a new runner from it: the root
[README](../../README.md#adding-a-plugin) says how to make a plugin of it,
and [runner/concourse](../../runner/concourse) is a complete one.

```sh
bazel run //templates/runner:test     # its tests, the kit among them
bazel run //templates/runner:lint
bazel run //:templates                # every template built and run through the kit
```

The kit by hand, in this directory:

```sh
go build -trimpath -o example-runner ./cmd/example-runner
go tool pipelinegen plugin test ./example-runner --tree testdata/kit --config testdata/kit/config.json
```
