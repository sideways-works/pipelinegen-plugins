# Security

## How to report a vulnerability

Please report a vulnerability privately, through the git host's private
vulnerability reporting: on GitHub, the repository's Security tab,
"Report a vulnerability". Don't open a public issue for it.

Say what is affected (gorm-correlator and the version), how it can be
used, and how to reproduce it.

## What counts

gorm-correlator runs in CI, in pipelinegen's planning job, on the code
of the repository under test, a pull request's included. It type-checks
that code through the go command and never runs it. Reports we
particularly want:

- a tree that makes gorm-correlator run the tree's code, or any program
  other than the go command, or makes either reach the network;
- a tree that makes gorm-correlator read or write a file outside the
  tree, the module cache and the directories pipelinegen gives it;
- a token or other secret from the request or the environment reaching
  an answer or a message;
- a statement that reaches a table without gorm-correlator linking it or
  saying it can't tell, so that a plan selects fewer tests than a change
  reaches without saying so.
