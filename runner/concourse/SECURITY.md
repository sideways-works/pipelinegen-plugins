# Security

## How to report a vulnerability

Please report a vulnerability privately, through the git host's private
vulnerability reporting: on GitHub, the repository's Security tab, "Report
a vulnerability". Don't open a public issue for it.

Say what is affected (concourse-runner and the version), how it can be
used, and how to reproduce it.

## What counts

concourse-runner writes the Concourse pipeline that tests a repository's
pull requests and branches. That pipeline holds a GitHub token that can
write to the repository, and it runs the code of every pull request it
tests. Reports we particularly want:

- a way for a pull request, especially one from a fork, to get the token
  or a secret the repository didn't declare for its tasks, or to run its
  own code in a step that holds a token with more than read access;
- a token or other secret reaching a log, a URL, a command line, a commit
  status, the pull request comment, or the pipeline's config as anything
  but a `((name))` reference;
- a release download in a generated step that runs without its checksum
  being checked;
- a config or include file that makes concourse-runner write a pipeline
  that hands a secret to a task that didn't declare it.
