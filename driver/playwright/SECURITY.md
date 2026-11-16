# Security

## How to report a vulnerability

Please report a vulnerability privately, through the git host's private
vulnerability reporting: on GitHub, the repository's Security tab, "Report
a vulnerability". Don't open a public issue for it.

Say what is affected (playwright-driver or `marks.mjs`, and the version),
how it can be used, and how to reproduce it.

## What counts

playwright-driver runs in CI, in the batch tasks of a pipeline that
pipelinegen generates, and runs the Playwright tests of the repository
under test, a pull request's included, after installing the project's
packages with `npm ci`. Running that code is its job. Reports we
particularly want:

- a token or other secret from the request reaching an answer, a message
  or a log line of the driver's own;
- a spec file reported as passed when its tests failed or didn't run;
- a recording that holds another spec file's coverage, or a mark from
  `marks.mjs` that lets a spec file go on before its boundary was
  answered, so that later plans select fewer tests than a change reaches
  without saying so.
