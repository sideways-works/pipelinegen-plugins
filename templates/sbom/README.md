# example-sbom

A minimal sbom plugin for
[pipelinegen](https://github.com/sideways-works/pipelinegen): it lists
every note (a `*.note` file whose first line is `# note`) as a unit,
`note:<path>`, with a digest that changes when the note's text does. It
answers `describe`, `detect` and `units`, each per file.

It passes pipelinegen's conformance kit on its fixture,
`testdata/kit`. Start a new sbom from it: the root
[README](../../README.md#adding-a-plugin) says how to make a plugin of it.

```sh
bazel run //templates/sbom:test     # its tests, the kit among them
bazel run //templates/sbom:lint
bazel run //:templates            # every template built and run through the kit
```

The kit by hand, in this directory:

```sh
go build -trimpath -o example-sbom ./cmd/example-sbom
go tool pipelinegen plugin test ./example-sbom --tree testdata/kit
```
