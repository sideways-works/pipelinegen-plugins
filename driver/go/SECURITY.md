# Security

## How to report a vulnerability

Please report a vulnerability privately, through the git host's private
vulnerability reporting: on GitHub, the repository's Security tab, "Report
a vulnerability". Don't open a public issue for it.

Say what is affected (go-driver and the version), how it can be used, and
how to reproduce it.

## What counts

go-driver runs in CI, in the batch tasks of a pipeline that pipelinegen
generates, and runs the tests of the repository under test, a pull
request's included, with `go test`. Running that code is its job; the
tests run with whatever the batch's environment holds. Reports we
particularly want:

- a token or other secret from the request reaching an answer, a message
  or a log line of the driver's own;
- a slice reported as passed when its tests failed or didn't run;
- a recording that holds another slice's coverage, or misses some of its
  own, so that later plans select fewer tests than a change reaches
  without saying so.
