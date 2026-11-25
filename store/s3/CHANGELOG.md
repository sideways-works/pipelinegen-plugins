# Changelog

Every release of s3-store, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-11-25

### Added

- The store: each record's versions as objects written once, its head replaced only under `If-Match`, listings that read only the heads that changed (requires pipelinegen v0.11.0)
