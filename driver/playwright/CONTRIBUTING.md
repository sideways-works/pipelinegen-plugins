# Contributing to playwright-driver

Thank you for wanting to help. This page says how to set up a working
copy, what every change has to pass, and how commits are written. What
playwright-driver runs, and how it bundles and records, is in the
[README](README.md). The protocol it speaks is in pipelinegen's book:
[drivers](https://github.com/sideways-works/pipelinegen/blob/main/docs/concepts/drivers.md)
and [writing a driver](https://github.com/sideways-works/pipelinegen/blob/main/docs/writing-plugins/driver.md).

## Reporting a problem

Open an issue on the repository with what you ran, what you expected and
what happened, and the version playwright-driver reports (`echo
'{"protocol": 1}' | playwright-driver describe`). For a spec file that got
the wrong result or no recording, the step's settings, the Playwright
version, whether the run was bundled or recording, and the output the
driver logged say most of what we need. A security problem goes through
the private route in [SECURITY.md](SECURITY.md), never an issue.

## A working copy

You need Go, golangci-lint and Node at the versions in `.tool-versions`,
npm, and git.

```sh
git clone https://github.com/sideways-works/pipelinegen-driver-playwright.git
cd pipelinegen-driver-playwright
go build -trimpath -o playwright-driver ./cmd/playwright-driver
```

## What every change passes

```sh
GOFLAGS=-trimpath go test ./...
golangci-lint run ./...                  # the repository's .golangci.yml
```

- The tests run the driver on copies of the fixture project in
  `driver/testdata/web`, three spec files whose tests need no browser.
  The driver installs the fixture's `@playwright/test` with `npm ci`, so
  the first run needs the npm registry; npm's downloads are kept in
  `playwright-driver-test` under your user cache directory, or in the
  directory `PLAYWRIGHT_DRIVER_TEST_CACHE` names.
- The tests include pipelinegen's conformance kit: `TestTheKit` builds the
  driver and runs `go tool pipelinegen plugin test` on it with the
  fixture, with the pipelinegen that `go.mod` requires as a tool. By hand:
  `pipelinegen plugin test ./playwright-driver --tree driver/testdata/web`.
- `marks.mjs` is the module a suite's own coverage fixture imports to
  mark each spec file's start and end. The fixture's
  `driver/testdata/web/e2e/marks.mjs` must stay byte for byte the same
  file, and a test checks that.
- A change to what playwright-driver runs comes with its docs: the README
  describes playwright-driver as it is on `main`.

## Commits and pull requests

A commit message is one conventional subject line of at most 72
characters, such as `fix: npm ci's message names the tree's root when dir
is unset`, with no body and no trailers. The types are `feat`, `fix`,
`perf`, `refactor`, `test`, `docs`, `build`, `ci` and `chore`. Keep
commits small and each one passing.

Open a pull request against `main`.

## Releases

Releases are cut by the maintainers with pipelinegen's `ci/release.sh`,
as its [release guide](https://github.com/sideways-works/pipelinegen/blob/main/docs/releases.md)
describes, from a tag `vX.Y.Z` on `main`. A candidate is tagged
`vX.Y.Z-rc.N` and published as a prerelease that is never marked the
latest. A published release never changes; a wrong one gets a new
version.

## Licence

playwright-driver is licensed under the [Apache License 2.0](LICENSE). By
contributing you agree that your contribution is licensed the same way.
