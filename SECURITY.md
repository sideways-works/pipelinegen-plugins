# Security

## How to report a vulnerability

Please report a vulnerability privately, through the git host's private
vulnerability reporting: on GitHub, the repository's Security tab, "Report
a vulnerability". Don't open a public issue for it.

Say what is affected (the plugin, by its program's name, and the version),
how it can be used, and how to reproduce it.

## What counts

Each plugin runs where pipelinegen calls it. The sbom plugins and the
correlators run while a pipeline plans or records, the drivers in a
suite's batch tasks, the runner when the pipeline is made, and the
connectors for each exchange with a git host. Each section below says
where a plugin runs and what it touches, then lists the reports we most
want for it.

### connector/git

git-connector runs in CI, in the steps of a pipeline that pipelinegen
generates, and picks the proposed changes of a plain git server among its
branches. It holds no token and sends nothing: pipelinegen checks each
request it asks for, then sends it with the tokens.

- a change that isn't one of the branches pipelinegen listed, or not at
  the commit it listed, or a branch the config's patterns don't name,
  reported as a proposed change;
- a change id that differs from the one pipelinegen's records hold for
  the branch, which would lose or mix up a change's earlier runs;
- a token or other secret reaching an answer, a message or a log line of
  the connector's own.

### connector/github

github-connector runs in CI, in the steps of a pipeline that pipelinegen
generates. It lists a repository's pull requests, sets commit statuses
and keeps a comment. It holds no token and sends nothing: pipelinegen
checks each request it asks for, then sends it with the tokens.

- a pull request from a fork, or from a repository the connector can't
  tell, reported as coming from the repository itself: its code would run
  with the pipeline's credentials;
- a request it asks for that reaches a host other than the repository's
  API, or a body that carries more than the call needs;
- a comment that isn't pipelinegen's, edited or deleted;
- a token or other secret reaching an answer, a message or a log line of
  the connector's own.

### correlator/go

go-correlator runs in CI, in pipelinegen's planning job and in the jobs
that run a repository's tests, on the code of the repository under test,
a pull request's included. It type-checks that code through the go
command with `GOPROXY=off` and `GOTOOLCHAIN=local`, and never runs it.
Its `cover` module is linked into the repository's own binaries, in
coverage builds only.

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

### correlator/gorm

gorm-correlator runs in CI, in pipelinegen's planning job, on the code
of the repository under test, a pull request's included. It type-checks
that code through the go command and never runs it.

- a tree that makes gorm-correlator run the tree's code, or any program
  other than the go command, or makes either reach the network;
- a tree that makes gorm-correlator read or write a file outside the
  tree, the module cache and the directories pipelinegen gives it;
- a token or other secret from the request or the environment reaching
  an answer or a message;
- a statement that reaches a table without gorm-correlator linking it or
  saying it can't tell, so that a plan selects fewer tests than a change
  reaches without saying so.

### correlator/locale

locale-correlator runs in CI, in the jobs that run a repository's tests.
Its recorders run inside those tests: `gorecord` in the repository's Go
programs, built with its tag, and the JavaScript recorders in the tests'
Node and browser. Its protocol calls read what the recorders wrote, and
start nothing.

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

### correlator/oapi-codegen

oapi-codegen-correlator runs in CI, in pipelinegen's planning job and in
the jobs that run a repository's tests, on the code, the contract and
the request logs of the repository under test, a pull request's
included. It reads those files and never runs them.

- a tree or a request log that makes oapi-codegen-correlator run code,
  start a process or reach the network;
- a tree or a recording that makes it read or write a file outside the
  tree and the directories pipelinegen gives it;
- a token or other secret from the request, or one a request log holds,
  reaching an answer or a message;
- an operation a test requested that oapi-codegen-correlator binds to no
  slice without saying it can't tell, so that a plan selects fewer tests
  than a change reaches without saying so.

### correlator/react

react-correlator runs in CI, in pipelinegen's planning job and in the jobs
that run a repository's browser tests, on the code of the repository under
test, a pull request's included. Its command parses that code and the
recordings and builds a test run left, and never runs them. Its Playwright
fixture runs inside the repository's tests and writes each spec file's
coverage.

- a tree, a build or a recording that makes the command run code of the
  repository's, start a process or reach the network;
- a tree or a recording that makes the command read or write a file
  outside the tree and the directories pipelinegen gives it;
- a way for the fixture to send a page's coverage or content anywhere but
  the coverage directory pipelinegen names;
- a token or other secret from the request or the environment reaching an
  answer or a message;
- a change that a test can observe but that react-correlator binds to
  none of the tests that can, without saying it can't tell, so that a plan
  selects fewer tests than the change reaches without saying so.

### correlator/sql

sql-correlator runs in CI, in the jobs that run a repository's tests, on
the statement log the tests' PostgreSQL server writes. It reads that log
and never connects to a database.

- a statement log that makes sql-correlator run code, start a process or
  reach the network;
- a log or a recording that makes it read or write a file outside the
  directories pipelinegen gives it;
- a token, a password or another secret, from the request or from a
  logged statement, reaching an answer or a message;
- a table a test touched that sql-correlator leaves out of the slice's
  observation without saying it can't tell, so that a plan selects fewer
  tests than a change reaches without saying so.

### driver/bazel

bazel-driver runs in CI, in the batch tasks of a pipeline that pipelinegen
generates, and runs the test targets of the repository under test, a pull
request's included, with Bazel. Running those tests is its job.

- a token or other secret from the request reaching an answer, a message
  or a log line of the driver's own;
- a target reported as passed when it failed, didn't build or didn't run,
  for example from a result left over from an earlier invocation;
- a stack suite's test replayed from Bazel's cache, or a recording that
  holds another slice's coverage, so that a plan selects fewer tests than
  a change reaches without saying so.

### driver/go

go-driver runs in CI, in the batch tasks of a pipeline that pipelinegen
generates, and runs the tests of the repository under test, a pull
request's included, with `go test`. Running that code is its job; the
tests run with whatever the batch's environment holds.

- a token or other secret from the request reaching an answer, a message
  or a log line of the driver's own;
- a slice reported as passed when its tests failed or didn't run;
- a recording that holds another slice's coverage, or misses some of its
  own, so that later plans select fewer tests than a change reaches
  without saying so.

### driver/playwright

playwright-driver runs in CI, in the batch tasks of a pipeline that
pipelinegen generates, and runs the Playwright tests of the repository
under test, a pull request's included, after installing the project's
packages with `npm ci`. Running that code is its job.

- a token or other secret from the request reaching an answer, a message
  or a log line of the driver's own;
- a spec file reported as passed when its tests failed or didn't run;
- a recording that holds another spec file's coverage, or a mark from
  `marks.mjs` that lets a spec file go on before its boundary was
  answered, so that later plans select fewer tests than a change reaches
  without saying so.

### driver/shell

shell-driver runs a suite's scripts in CI, on the tree under test, with
the environment pipelinegen gives it.

- a token or other secret of the tool's reaching a script's environment
  without the suite declaring it, or reaching a log or a result;
- a way for a change under test to choose the script a step runs, when
  the steps come from the trusted config;
- a slice reported as passed whose script or tests did not pass;
- a process a step started that outlives its batch.

### runner/concourse

concourse-runner writes the Concourse pipeline that tests a repository's
pull requests and branches. That pipeline holds a GitHub token that can
write to the repository, and it runs the code of every pull request it
tests.

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

### sbom/go

go-sbom runs in CI, in pipelinegen's planning job, on the files of the
repository under test, a pull request's included. It only reads files: it
never runs the go command and never touches the network.

- a tree or a module cache that makes go-sbom run code, start a process
  or reach the network;
- a tree that makes go-sbom read or write a file outside the tree, the
  module cache and the directories pipelinegen gives it;
- a token or other secret from the request reaching an answer or a
  message;
- a change that alters what the compiler reads of a declaration or a
  module but leaves its digest the same, so that a plan selects fewer
  tests than the change reaches without saying so.

### sbom/locale

locale-sbom runs in CI, in pipelinegen's planning job, on the message
catalogs of the repository under test, a pull request's included. It
reads files and answers JSON: it starts nothing and connects to nothing.

- a catalog that makes locale-sbom run code, start a process or reach
  the network;
- a tree, a symlink among them, that makes locale-sbom read or write a
  file outside the tree and the directories pipelinegen gives it;
- a secret, from a catalog that holds one or from the request, reaching
  an answer or a message;
- a change to what a key renders that leaves its digest the same, so
  that a plan selects fewer tests than the change reaches without saying
  so.

### sbom/npm

npm-sbom runs in CI, in pipelinegen's planning job, on the files of the
repository under test, a pull request's included. It only reads
lockfiles and manifests: it installs nothing, runs no package script and
never touches the network.

- a lockfile or manifest that makes npm-sbom run code, start a process
  or reach the network;
- a tree that makes npm-sbom read or write a file outside the tree and
  the directories pipelinegen gives it;
- a token or other secret from the request reaching an answer or a
  message;
- a change to what a workspace installs that leaves every digest the
  same, so that a plan selects fewer tests than the change reaches
  without saying so.

### sbom/openapi

openapi-sbom runs in CI, in pipelinegen's planning job, on the contracts
of the repository under test, a pull request's included. It only reads
files and never touches the network.

- a contract that makes openapi-sbom run code, start a process or reach
  the network;
- a contract or a `$ref` that makes openapi-sbom read or write a file
  outside the tree and the directories pipelinegen gives it;
- a token or other secret from the request reaching an answer or a
  message;
- a change that alters what a server or a generated client does but
  leaves every digest the same, so that a plan selects fewer tests than
  the change reaches without saying so.

### sbom/pg

pg-sbom runs in CI, in pipelinegen's planning job, on the migration
files of the repository under test, a pull request's included. It parses
them with PostgreSQL's parser, compiled to WebAssembly and run
in-process, and never runs them: it starts no database and connects to
nothing.

- a migration that makes pg-sbom run code outside its WebAssembly
  sandbox, start a process or reach the network;
- a tree that makes pg-sbom read or write a file outside the tree and
  the directories pipelinegen gives it;
- a token or other secret from the request reaching an answer or a
  message;
- a change to what the migrations build that leaves every digest the
  same, so that a plan selects fewer tests than the change reaches
  without saying so.

### sbom/ts

ts-sbom runs in CI, in pipelinegen's planning job, on the files of the
repository under test, a pull request's included. It parses those files
and never runs them.

- a tree that makes ts-sbom run code of the repository's, start a process
  or reach the network;
- a tree that makes ts-sbom read or write a file outside the tree and the
  directories pipelinegen gives it;
- a token or other secret from the request reaching an answer or a
  message;
- a change that alters what a unit does but leaves its digest the same,
  so that a plan selects fewer tests than the change reaches without
  saying so.
