# Changelog

Every release of piperun-runner, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- The runner: one pipeline per run in `piperun.yaml`, whose planning jobs emit each suite's prepare, the pool of its selected slices and its end from the templates the file declares; branch, continuous, timer and manual runs; suites with drivers' steps (requires pipelinegen v0.9.1 for a config without a `concourse` section)
