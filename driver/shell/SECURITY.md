# Security

## How to report a vulnerability

Please report a vulnerability privately, through the git host's private
vulnerability reporting: on GitHub, the repository's Security tab, "Report
a vulnerability". Don't open a public issue for it.

Say what is affected (shell-driver, and the version), how it can be used,
and how to reproduce it.

## What counts

shell-driver runs a suite's scripts in CI, on the tree under test, with
the environment pipelinegen gives it. Reports we particularly want:

- a token or other secret of the tool's reaching a script's environment
  without the suite declaring it, or reaching a log or a result;
- a way for a change under test to choose the script a step runs, when
  the steps come from the trusted config;
- a slice reported as passed whose script or tests did not pass;
- a process a step started that outlives its batch.
