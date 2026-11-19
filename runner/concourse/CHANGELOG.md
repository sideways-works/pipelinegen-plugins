# Changelog

Every release of concourse-runner, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- A batch task that errors (a container or volume that couldn't be set up, a worker lost) runs once more, the same task with the same inputs, outputs and params, in its `on_error`, and the batch's `batch-end` reads the second attempt's outputs. A batch whose tests failed, that hit its timeout, or that was aborted, runs once. The batch sits in a `try` whose `ensure` is `batch-end`, which fails the step when a slice didn't pass: a failed test stays red, a second error leaves its slices errored and fails the step, and a retry that passed passes it, where an errored step would have stayed errored under its `on_error` alone

- With a pipelinegen that writes the bookkeeping files, a batch's end and the suite job's end fetch the tools they call themselves, pipelinegen, the correlators, the connectors and the runner, never a driver or bazelisk, and keep them as their task's cache `tools` on the worker, instead of taking the `start` task's `tools`. A bookkeeping placement then streams no tools: a task fetches them once per worker and job, checked against their pins every build. An end that can't fetch them fails (exit 4) rather than leave its suite without an end

- A batch's end takes the bookkeeping files instead of the tree under test when the pinned pipelinegen writes them (a release from v0.4.2 on, or a build of a commit): the suite job's start task writes them with `pipelinegen slice bookkeeping-files --root source --out bookkeeping`, its new output `bookkeeping`, and the end's root and input are `bookkeeping`. An end placed on a worker of its own (a `bookkeeping` placement) then streams the config instead of the whole checkout. A recording run of a suite whose batch is its own task file keeps `source` at its ends, as they observe its slices in the tree; with an older pipelinegen every end keeps `source`

## [0.4.1] - 2026-11-19

The first release from the plugins repository. Its tag `runner/concourse/v0.4.0` exists without a release.

### Added

- The batch and prepare tasks of a suite run by drivers pass `--tools tools` to `slice run` and `slice prepare`, so the tools directory, with pipelinegen and bazelisk, comes first on the `PATH` of the drivers and their tests whatever the pinned pipelinegen's default
- A plugin pinned with a `path`, in a repository that holds several, is fetched from the release tagged `<path>/vX.Y.Z` and named by its path (`driver/go` holds `go-driver`); pinned by a commit it is built in that directory. The runner itself may be pinned so, and the bootstrap fetches it from that tag
- A plugin's `repository` may be a git URL (`https://github.com/owner/name`, `git@host:owner/name.git`) as well as `owner/name`. GitHub's releases are read through the API with the read token as before, GitLab's downloaded from their links with no token; a release on another git server is refused. Checksums are checked as before
- Several runners: with a spec that names this runner (`runner`), the `pipeline` job and the stale check write the pipeline with `--for <runner>`, the bootstrap and the steps fetch no other runner, a placement rule naming a run another runner owns (`other_runs`), or a suite none of this runner's runs has, is accepted and places nothing, and a spec with no runs writes the `pipeline` job alone, its rules for planning, suites and bookkeeping not refused for placing nothing

- Pins by `sums`: the bootstrap and the steps check a release pinned by the checksum of its `SHA256SUMS` (pipelinegen's `release: {version, sums}`, as `pipelinegen plugin add` writes it), the runner's own pin and `concourse.planner.bootstrap.release` included. The list is checked against the pin, then the file against its line; the list is kept beside each plugin as `<name>.SHA256SUMS`, where pipelinegen reads it. A pin by `sha256` is checked as before

### Changed

- A build of a plugin, or of the runner, from a commit runs with `GOWORK=off`, as its release is built: a `go.work` at the repository's root no longer takes part
- The pipeline is refused when the runner's `repository` is not on github.com, where alone the bootstrap fetches it
