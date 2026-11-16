# Contributing to locale-correlator

Thank you for wanting to help. This page says how to set up a working
copy, what every change has to pass, and how commits are written. What
locale-correlator does, what a recording holds, its recorders for Go and
JavaScript, and what it never sees, is in the [README](README.md). The
protocol it speaks is in pipelinegen's book:
[the plugin protocol](https://github.com/sideways-works/pipelinegen/blob/main/docs/plugins.md)
and [writing a correlator](https://github.com/sideways-works/pipelinegen/blob/main/docs/writing-plugins/correlator.md).

## Reporting a problem

Open an issue on the repository with what you ran, what you expected and
what happened, and the version locale-correlator reports (`echo
'{"protocol": 1}' | locale-correlator describe`). For a key a test
rendered that wasn't recorded, the recorder you use (gorecord, the
vitest setup or the Playwright fixture), the i18next version and the
`why` answer for the slice in question say most of what we need. A
security problem goes through the private route in
[SECURITY.md](SECURITY.md), never an issue.

## A working copy

You need Go and golangci-lint at the versions in `.tool-versions`, and
git. `go.mod` takes locale-sbom from `../locale-sbom`, so clone it
beside this one under that name. The recorders' JavaScript tests also
need a `node_modules` with i18next, vitest and @playwright/test, and one
of them a headless Chromium.

```sh
git clone https://github.com/sideways-works/pipelinegen-sbom-locale.git locale-sbom
git clone https://github.com/sideways-works/pipelinegen-correlator-locale.git
cd pipelinegen-correlator-locale
go build -trimpath -o locale-correlator ./cmd/locale-correlator
```

## What every change passes

```sh
GOFLAGS=-trimpath go test ./...
(cd gorecord && GOFLAGS=-trimpath go test ./... && golangci-lint run ./...)
golangci-lint run ./...                  # the repository's .golangci.yml
LOCALE_NODE_MODULES=<node_modules> go test ./cmd/locale-correlator
LOCALE_BROWSER=1 LOCALE_NODE_MODULES=<node_modules> go test ./cmd/locale-correlator -run Playwright
pipelinegen plugin test ./locale-correlator --tree testdata/kit
```

- `gorecord` is a module of its own, which a repository's Go code links
  in its recording builds, so `./...` stops at it and it is tested and
  linted on its own.
- Without `LOCALE_NODE_MODULES` the JavaScript recorders' tests are
  skipped, and without `LOCALE_BROWSER=1` the one that starts a browser
  is; run them before a change to the recorders. The README names the
  versions they were tested with.
- The last line is pipelinegen's conformance kit. It calls the
  locale-correlator you built the way pipelinegen does, on the fixture
  tree in `testdata/kit`. `PIPELINEGEN=<pipelinegen> go test
  ./cmd/locale-correlator -run TestKit` runs the same kit from the
  tests, which skip it when `PIPELINEGEN` names no binary. Take
  `pipelinegen` from [its
  releases](https://github.com/sideways-works/pipelinegen/blob/main/docs/tutorial/install.md).
- A change to what locale-correlator records comes with its docs: the
  README describes locale-correlator as it is on `main`.

## Commits and pull requests

A commit message is one conventional subject line of at most 72
characters, such as `docs: the tagged pair needs go-correlator to bind a
left-out file alone`, with no body and no trailers. The types are
`feat`, `fix`, `perf`, `refactor`, `test`, `docs`, `build`, `ci` and
`chore`. Keep commits small and each one passing.

Open a pull request against `main`.

## Releases

Releases are cut by the maintainers with pipelinegen's `ci/release.sh`,
as its [release
guide](https://github.com/sideways-works/pipelinegen/blob/main/docs/releases.md)
describes, from a tag `vX.Y.Z` on `main`. The `gorecord` module is
released with it: the same commit also gets the tag `gorecord/vX.Y.Z`. A
candidate is tagged `vX.Y.Z-rc.N` and published as a prerelease that is
never marked the latest. A published release never changes; a wrong one
gets a new version.

## Licence

locale-correlator is licensed under the [Apache License 2.0](LICENSE),
and so is the `gorecord` module, which carries its own copy of the
licence because it is fetched apart from the rest. By contributing you
agree that your contribution is licensed the same way.
