# Security

## How to report a vulnerability

Please report a vulnerability privately, through the git host's private
vulnerability reporting: on GitHub, the repository's Security tab,
"Report a vulnerability". Don't open a public issue for it.

Say what is affected (pg-sbom and the version), how it can be used, and
how to reproduce it.

## What counts

pg-sbom runs in CI, in pipelinegen's planning job, on the migration
files of the repository under test, a pull request's included. It parses
them with PostgreSQL's parser, compiled to WebAssembly and run
in-process, and never runs them: it starts no database and connects to
nothing. Reports we particularly want:

- a migration that makes pg-sbom run code outside its WebAssembly
  sandbox, start a process or reach the network;
- a tree that makes pg-sbom read or write a file outside the tree and
  the directories pipelinegen gives it;
- a token or other secret from the request reaching an answer or a
  message;
- a change to what the migrations build that leaves every digest the
  same, so that a plan selects fewer tests than the change reaches
  without saying so.
