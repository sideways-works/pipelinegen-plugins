# Changelog

Every release of concourse-runner, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- The batch and prepare tasks of a suite run by drivers pass `--tools tools` to `slice run` and `slice prepare`, so the tools directory, with pipelinegen and bazelisk, comes first on the `PATH` of the drivers and their tests whatever the pinned pipelinegen's default
