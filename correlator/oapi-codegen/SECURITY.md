# Security

## How to report a vulnerability

Please report a vulnerability privately, through the git host's private
vulnerability reporting: on GitHub, the repository's Security tab,
"Report a vulnerability". Don't open a public issue for it.

Say what is affected (oapi-codegen-correlator and the version), how it
can be used, and how to reproduce it.

## What counts

oapi-codegen-correlator runs in CI, in pipelinegen's planning job and in
the jobs that run a repository's tests, on the code, the contract and
the request logs of the repository under test, a pull request's
included. It reads those files and never runs them. Reports we
particularly want:

- a tree or a request log that makes oapi-codegen-correlator run code,
  start a process or reach the network;
- a tree or a recording that makes it read or write a file outside the
  tree and the directories pipelinegen gives it;
- a token or other secret from the request, or one a request log holds,
  reaching an answer or a message;
- an operation a test requested that oapi-codegen-correlator binds to no
  slice without saying it can't tell, so that a plan selects fewer tests
  than a change reaches without saying so.
