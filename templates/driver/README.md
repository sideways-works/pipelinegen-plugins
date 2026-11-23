# example-driver

A minimal driver plugin for
[pipelinegen](https://github.com/sideways-works/pipelinegen): it runs
slices that are shell test files (`*_test.sh`) with `sh` in the tree,
one after another whether bundled or not, and passes a slice whose
script exits 0. It judges by the exit code alone (`results: exit`) and
records nothing.

It passes pipelinegen's conformance kit on its fixture,
`testdata/kit`. Start a new driver from it: the root
[CONTRIBUTING](../../CONTRIBUTING.md#adding-a-plugin) says how to make a plugin of it.

```sh
bazel run //templates/driver:test     # its tests, the kit among them
bazel run //templates/driver:lint
bazel run //:templates            # every template built and run through the kit
```

The kit by hand, in this directory:

```sh
go build -trimpath -o example-driver ./cmd/example-driver
go tool pipelinegen plugin test ./example-driver --tree testdata/kit
```
