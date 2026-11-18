# Security

## How to report a vulnerability

Please report a vulnerability privately, through the git host's private
vulnerability reporting: on GitHub, the repository's Security tab,
"Report a vulnerability". Don't open a public issue for it.

Say what is affected (locale-correlator and the version), how it can be
used, and how to reproduce it.

## What counts

locale-correlator runs in CI, in the jobs that run a repository's tests.
Its recorders run inside those tests: `gorecord` in the repository's Go
programs, built with its tag, and the JavaScript recorders in the tests'
Node and browser. Its protocol calls read what the recorders wrote, and
start nothing. Reports we particularly want:

- a way for a recorder to send what it records, or anything else,
  anywhere but the directory or log pipelinegen names, or to change what
  the code under test renders;
- a recording that makes the protocol calls read or write a file outside
  the directories pipelinegen gives them, run code or start a process;
- a token or other secret from the request, or a rendered text, reaching
  an answer or a message;
- a key a test looked up that locale-correlator leaves out without
  saying it can't tell, so that a plan selects fewer tests than a change
  reaches without saying so.
