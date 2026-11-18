# Security

## How to report a vulnerability

Please report a vulnerability privately, through the git host's private
vulnerability reporting: on GitHub, the repository's Security tab, "Report
a vulnerability". Don't open a public issue for it.

Say what is affected (react-correlator's command or its Playwright
fixture, and the version), how it can be used, and how to reproduce it.

## What counts

react-correlator runs in CI, in pipelinegen's planning job and in the jobs
that run a repository's browser tests, on the code of the repository under
test, a pull request's included. Its command parses that code and the
recordings and builds a test run left, and never runs them. Its Playwright
fixture runs inside the repository's tests and writes each spec file's
coverage. Reports we particularly want:

- a tree, a build or a recording that makes the command run code of the
  repository's, start a process or reach the network;
- a tree or a recording that makes the command read or write a file
  outside the tree and the directories pipelinegen gives it;
- a way for the fixture to send a page's coverage or content anywhere but
  the coverage directory pipelinegen names;
- a token or other secret from the request or the environment reaching an
  answer or a message;
- a change that a test can observe but that react-correlator binds to
  none of the tests that can, without saying it can't tell, so that a plan
  selects fewer tests than the change reaches without saying so.
