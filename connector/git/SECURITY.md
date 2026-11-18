# Security

## How to report a vulnerability

Please report a vulnerability privately, through the git host's private
vulnerability reporting: on GitHub, the repository's Security tab, "Report
a vulnerability". Don't open a public issue for it.

Say what is affected (git-connector and the version), how it can be used, and
how to reproduce it.

## What counts

git-connector runs in CI, in the steps of a pipeline that pipelinegen generates,
and picks the proposed changes of a plain git server among its branches. It holds no token and sends nothing: pipelinegen sends the
requests it asks for, with the tokens, and checks each one first. Reports
we particularly want:

- a change that isn't one of the branches pipelinegen listed, or not at
  the commit it listed, or a branch the config's patterns don't name,
  reported as a proposed change;
- a change id that differs from the one pipelinegen's records hold for
  the branch, which would lose or mix up a change's earlier runs;
- a token or other secret reaching an answer, a message or a log line of
  the connector's own.
