# Security

## How to report a vulnerability

Please report a vulnerability privately, through the git host's private
vulnerability reporting: on GitHub, the repository's Security tab, "Report
a vulnerability". Don't open a public issue for it.

Say what is affected (go-sbom and the version), how it can be used, and
how to reproduce it.

## What counts

go-sbom runs in CI, in pipelinegen's planning job, on the files of the
repository under test, a pull request's included. It only reads files: it
never runs the go command and never touches the network. Reports we
particularly want:

- a tree or a module cache that makes go-sbom run code, start a process
  or reach the network;
- a tree that makes go-sbom read or write a file outside the tree, the
  module cache and the directories pipelinegen gives it;
- a token or other secret from the request reaching an answer or a
  message;
- a change that alters what the compiler reads of a declaration or a
  module but leaves its digest the same, so that a plan selects fewer
  tests than the change reaches without saying so.
