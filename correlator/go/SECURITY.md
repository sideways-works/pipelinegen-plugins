# Security

## How to report a vulnerability

Please report a vulnerability privately, through the git host's private
vulnerability reporting: on GitHub, the repository's Security tab, "Report
a vulnerability". Don't open a public issue for it.

Say what is affected (go-correlator or its `cover` module, and the
version), how it can be used, and how to reproduce it.

## What counts

go-correlator runs in CI, in pipelinegen's planning job and in the jobs
that run a repository's tests, on the code of the repository under test,
a pull request's included. It type-checks that code through the go
command with `GOPROXY=off` and `GOTOOLCHAIN=local`, and never runs it.
Its `cover` module is linked into the repository's own binaries, in
coverage builds only. Reports we particularly want:

- a tree that makes go-correlator run the tree's code, or any program
  other than the go command, or makes it or the go command it starts
  reach the network;
- a tree or a recording that makes go-correlator read or write a file
  outside the tree and the directories pipelinegen gives it;
- a way for a binary that links `cover` to do anything beyond reading the
  request file in the directory `PIPELINEGEN_COVER_DIR` names and writing
  its coverage and its answers;
- a token or other secret from the request or the environment reaching an
  answer or a message;
- a change that a test can observe but that go-correlator binds to none of
  the tests that can, without saying it can't tell, so that a plan selects
  fewer tests than the change reaches without saying so.
