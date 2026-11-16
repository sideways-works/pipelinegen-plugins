# Security

## How to report a vulnerability

Please report a vulnerability privately, through the git host's private
vulnerability reporting: on GitHub, the repository's Security tab,
"Report a vulnerability". Don't open a public issue for it.

Say what is affected (npm-sbom and the version), how it can be used, and
how to reproduce it.

## What counts

npm-sbom runs in CI, in pipelinegen's planning job, on the files of the
repository under test, a pull request's included. It only reads
lockfiles and manifests: it installs nothing, runs no package script and
never touches the network. Reports we particularly want:

- a lockfile or manifest that makes npm-sbom run code, start a process
  or reach the network;
- a tree that makes npm-sbom read or write a file outside the tree and
  the directories pipelinegen gives it;
- a token or other secret from the request reaching an answer or a
  message;
- a change to what a workspace installs that leaves every digest the
  same, so that a plan selects fewer tests than the change reaches
  without saying so.
