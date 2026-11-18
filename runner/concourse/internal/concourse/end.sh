# Runs in the job's ensure, on an abort too. A build aborted while its start
# task waited for a worker never had the tools: fetching them here still ends
# its suite, so a continuous run's next round need not wait for it.
set -eu
if ! test -d trigger; then
  echo "no trigger: the build broke before it could report" >&2
  exit 0
fi
if ! test -x tools/pipelinegen; then
  echo "no tools: the build broke before its start task had them; fetching them to end the suite" >&2
  set +e
  # a subshell, so the bootstrap's own exit and set -e end only it
  (
#BOOTSTRAP#
  )
  rc=$?
  set -e
  if [ "$rc" -ne 0 ] || ! test -x tools/pipelinegen; then
    echo "the tools could not be fetched (exit $rc): the build broke before it could report" >&2
    # the job's on_error or on_abort hook already posted its statuses
    exit 0
  fi
fi
rc=0
# only a suite's end that could not be recorded (4) fails the job: the
# suite's statuses show any other failure
tools/pipelinegen report suite --root trigger --batches batches || rc=$?
case "$rc" in
0) ;;
4) exit 4 ;;
*) echo "the suite's end exited $rc; its statuses say why" >&2 ;;
esac
