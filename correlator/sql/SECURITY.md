# Security

## How to report a vulnerability

Please report a vulnerability privately, through the git host's private
vulnerability reporting: on GitHub, the repository's Security tab,
"Report a vulnerability". Don't open a public issue for it.

Say what is affected (sql-correlator and the version), how it can be
used, and how to reproduce it.

## What counts

sql-correlator runs in CI, in the jobs that run a repository's tests, on
the statement log the tests' PostgreSQL server writes. It reads that log
and never connects to a database. Reports we particularly want:

- a statement log that makes sql-correlator run code, start a process or
  reach the network;
- a log or a recording that makes it read or write a file outside the
  directories pipelinegen gives it;
- a token, a password or another secret, from the request or from a
  logged statement, reaching an answer or a message;
- a table a test touched that sql-correlator leaves out of the slice's
  observation without saying it can't tell, so that a plan selects fewer
  tests than a change reaches without saying so.
