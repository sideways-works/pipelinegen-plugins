# Changelog

Every release of concourse-runner, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.8.0] - 2026-11-23

### Added

- With `plans.s3` in the config, the key of the plans' bucket goes as `PIPELINEGEN_PLANS_ACCESS_KEY_ID` and `PIPELINEGEN_PLANS_SECRET_ACCESS_KEY` to the steps that write a run's plan alone: a branch run's `plan` task and the `write` tasks of a pull request run's planning and sweep (requires pipelinegen v0.8.0)

## [0.7.0] - 2026-11-22

### Changed

- Every step's `timeout` has the wait for a worker on top of its own limit, `worker_wait_minutes` in the runner's config (480 by default, 0 for none). Concourse counts a step's wait for a worker inside its timeout, so with every worker at its limit of active tasks a batch failed without having started; pipelinegen's own steps and a driven suite's batches still end within their minutes, counted from their start

## [0.6.1] - 2026-11-21

### Fixed

- Fetching a plugin no longer hangs on a machine short of pipe space: git archive's padding after the tar is read to its end, so git exits (plugkit v0.7.1)

## [0.6.0] - 2026-11-20

### Changed

- Breaking: requires pipelinegen v0.7.0, whose plugin kind connector is now forge. A run whose changes come from a forge is read from the spec's `forge`, the capability is `forge_changes`, and the step that hands on such a run's change is `sweep-write --forge`. A pipelinegen before v0.7.0, which names the kind, the run's field and the capability connector, still gets its pipeline: the runner reads those names too

## [0.5.0] - 2026-11-20

### Added

- A plugin pinned by a zip (`zip: {url, sha256}`, pipelinegen v0.5.0 on) is fetched into `tools/plugins/<name>`: downloaded over https, checked against its sha256 before it is opened, and unpacked by pipelinegen's own store (plugkit's) into the job's cache under `plugins/<sha256>`, once per worker; its `linux/amd64` program is the one placed. A zip no pin names any more leaves the cache as a release does. The plugin lines carry a zip's url and sha256 after the other fields

### Changed

- With pipelinegen pinned by a release from v0.5.0 on, a plugin pinned by a commit, the runner included, is refused when the pipeline is written: pipelinegen builds such a plugin from its `manifest.yaml` in a container, which a task could run only privileged with a container runtime in its image, handing a pull request's code root on the worker. Build it into a zip with `pipelinegen plugin build` and pin the zip. With pipelinegen pinned by a commit, or by a release before v0.5.0, a commit's plugin is built with `go build` as before
- A runner pinned by a zip is refused when the pipeline is written: the bootstrap fetches the runner before anything that unpacks a zip runs. A zip pin with a pipelinegen release before v0.5.0, which can't read it, is refused too
- Requires pipelinegen v0.5.0 and plugkit v0.4.0

## [0.4.2] - 2026-11-20

### Changed

- A batch's end takes the bookkeeping files instead of the tree under test when the pinned pipelinegen writes them (a release from v0.4.2 on, or a build of a commit): the suite job's start task writes them with `pipelinegen slice bookkeeping-files --root source --out bookkeeping`, its new output `bookkeeping`, and the end's root and input are `bookkeeping`. An end placed on a worker of its own (a `bookkeeping` placement) then streams the config instead of the whole checkout. A recording run of a suite whose batch is its own task file keeps `source` at its ends, as they observe its slices in the tree; with an older pipelinegen every end keeps `source`
- With a pipelinegen that writes the bookkeeping files, a batch's end and the suite job's end fetch the tools they call themselves, pipelinegen, the correlators, the connectors and the runner, never a driver or bazelisk, and keep them as their task's cache `tools` on the worker, instead of taking the `start` task's `tools`. A bookkeeping placement then streams no tools: a task fetches them once per worker and job, checked against their pins every build. An end that can't fetch them fails (exit 4) rather than leave its suite without an end
- A batch task that errors (a container or volume that couldn't be set up, a worker lost) runs once more, the same task with the same inputs, outputs and params, in its `on_error`, and the batch's `batch-end` reads the second attempt's outputs. A batch whose tests failed, that hit its timeout, or that was aborted, runs once. The batch sits in a `try` whose `ensure` is `batch-end`, which fails the step when a slice didn't pass: a failed test stays red, a second error leaves its slices errored and fails the step, and a retry that passed passes it, where an errored step would have stayed errored under its `on_error` alone
- Requires pipelinegen v0.4.2, whose conformance kit knows `slice bookkeeping-files`

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
