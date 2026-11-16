# Security

## How to report a vulnerability

Please report a vulnerability privately, through the git host's private
vulnerability reporting: on GitHub, the repository's Security tab,
"Report a vulnerability". Don't open a public issue for it.

Say what is affected (locale-sbom and the version), how it can be used,
and how to reproduce it.

## What counts

locale-sbom runs in CI, in pipelinegen's planning job, on the message
catalogs of the repository under test, a pull request's included. It
reads files and answers JSON: it starts nothing and connects to nothing.
Reports we particularly want:

- a catalog that makes locale-sbom run code, start a process or reach
  the network;
- a tree, a symlink among them, that makes locale-sbom read or write a
  file outside the tree and the directories pipelinegen gives it;
- a secret, from a catalog that holds one or from the request, reaching
  an answer or a message;
- a change to what a key renders that leaves its digest the same, so
  that a plan selects fewer tests than the change reaches without saying
  so.
