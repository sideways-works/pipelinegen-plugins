# Changelog

Every release of concourse-runner, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- The batch and prepare tasks of a suite run by drivers pass `--tools tools` to `slice run` and `slice prepare`, so the tools directory, with pipelinegen and bazelisk, comes first on the `PATH` of the drivers and their tests whatever the pinned pipelinegen's default
- A plugin pinned with a `path`, in a repository that holds several, is fetched from the release tagged `<path>/vX.Y.Z` and named by its path (`driver/go` holds `go-driver`); pinned by a commit it is built in that directory. The runner itself may be pinned so, and the bootstrap fetches it from that tag
- A plugin's `repository` may be a git URL (`https://github.com/owner/name`, `git@host:owner/name.git`) as well as `owner/name`. GitHub's releases are read through the API with the read token as before, GitLab's downloaded from their links with no token; a release on another git server is refused. Checksums are checked as before
- Several runners: with a spec that names this runner (`runner`), the `pipeline` job and the stale check write the pipeline with `--for <runner>`, the bootstrap and the steps fetch no other runner, a placement rule naming a run another runner owns (`other_runs`) is accepted and places nothing, and a spec with no runs writes the `pipeline` job alone, its rules for planning, suites and bookkeeping not refused for placing nothing

### Changed

- A build of a plugin, or of the runner, from a commit runs with `GOWORK=off`, as its release is built: a `go.work` at the repository's root no longer takes part
- The pipeline is refused when the runner's `repository` is not on github.com, where alone the bootstrap fetches it
