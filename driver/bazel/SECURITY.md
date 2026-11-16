# Security

## How to report a vulnerability

Please report a vulnerability privately, through the git host's private
vulnerability reporting: on GitHub, the repository's Security tab, "Report
a vulnerability". Don't open a public issue for it.

Say what is affected (bazel-driver and the version), how it can be used,
and how to reproduce it.

## What counts

bazel-driver runs in CI, in the batch tasks of a pipeline that pipelinegen
generates, and runs the test targets of the repository under test, a pull
request's included, with Bazel. Running those tests is its job. Reports we
particularly want:

- a token or other secret from the request reaching an answer, a message
  or a log line of the driver's own;
- a target reported as passed when it failed, didn't build or didn't run,
  for example from a result left over from an earlier invocation;
- a stack suite's test replayed from Bazel's cache, or a recording that
  holds another slice's coverage, so that a plan selects fewer tests than
  a change reaches without saying so.
