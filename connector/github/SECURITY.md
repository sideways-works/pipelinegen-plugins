# Security

## How to report a vulnerability

Please report a vulnerability privately, through the git host's private
vulnerability reporting: on GitHub, the repository's Security tab, "Report
a vulnerability". Don't open a public issue for it.

Say what is affected (github-connector and the version), how it can be used, and
how to reproduce it.

## What counts

github-connector runs in CI, in the steps of a pipeline that pipelinegen generates,
and lists a repository's pull requests, sets commit statuses and keeps a comment. It holds no token and sends nothing: pipelinegen sends the
requests it asks for, with the tokens, and checks each one first. Reports
we particularly want:

- a pull request from a fork, or from a repository the connector can't
  tell, reported as coming from the repository itself: its code would run
  with the pipeline's credentials;
- a request it asks for that reaches a host other than the repository's
  API, or a body that carries more than the call needs;
- a comment that isn't pipelinegen's, edited or deleted;
- a token or other secret reaching an answer, a message or a log line of
  the connector's own.
