# Changelog

Every release of git-forge, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.5.0]

### Changed

- The plugin is now a forge, pipelinegen's new name for the kind connector: it lives in `forge/git`, its program is `git-forge`, its module `github.com/sideways-works/pipelinegen-plugins/forge/git`, and `describe` answers kind `forge`, which pipelinegen v0.6.0 requires. Its releases are tagged `forge/git/vX.Y.Z`; the last of `connector/git` was v0.4.0, and this release continues from there
