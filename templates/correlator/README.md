# example-correlator

A minimal correlator plugin for
[pipelinegen](https://github.com/sideways-works/pipelinegen): it links a
note's unit to each note it names on a `see: <path>` line, so a change
to a note reaches the notes that refer to it. Its units are the ones the
sbom template lists, spelled by the same small `note` package. It
answers `describe`, `detect` and `links`, each per file.

It passes pipelinegen's conformance kit on its fixture,
`testdata/kit`, and is the starting point for a new correlator: the root
[README](../../README.md#adding-a-plugin) says how to make a plugin of it.

```sh
bazel run //templates/correlator:test     # its tests, the kit among them
bazel run //templates/correlator:lint
bazel run //:templates            # every template built and run through the kit
```

The kit by hand, in this directory:

```sh
go build -trimpath -o example-correlator ./cmd/example-correlator
go tool pipelinegen plugin test ./example-correlator --tree testdata/kit
```
