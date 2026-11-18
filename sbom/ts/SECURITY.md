# Security

## How to report a vulnerability

Please report a vulnerability privately, through the git host's private
vulnerability reporting: on GitHub, the repository's Security tab, "Report
a vulnerability". Don't open a public issue for it.

Say what is affected (ts-sbom's command or its library, and the version),
how it can be used, and how to reproduce it.

## What counts

ts-sbom runs in CI, in pipelinegen's planning job, on the files of the
repository under test, a pull request's included. It parses those files
and never runs them. Reports we particularly want:

- a tree that makes ts-sbom run code of the repository's, start a process
  or reach the network;
- a tree that makes ts-sbom read or write a file outside the tree and the
  directories pipelinegen gives it;
- a token or other secret from the request reaching an answer or a
  message;
- a change that alters what a unit does but leaves its digest the same,
  so that a plan selects fewer tests than the change reaches without
  saying so.
