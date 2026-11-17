# The suite job's last task, in its ensure: it runs whatever the build did,
# aborted and errored builds included. A build that broke before its trigger
# was there has no commit to report to, and ends here.
# A build that broke before its start task had the tools, such as one
# aborted or errored while that task waited for a worker, fetches them here
# as the start task does, so its suite still ends and a continuous run's
# next round need not wait for it. Tools that can't be fetched leave the
# suite to its statuses, which the job's on_error or on_abort posted.
# It reports the suite from the trigger. A suite's end that could not be
# recorded (exit 4) fails the job; any other failure here is said and
# passes, since the suite's own statuses show it.
set -eu
if ! test -d trigger; then
  echo "no trigger: the build broke before it could report" >&2
  exit 0
fi
if ! test -x tools/pipelinegen; then
  echo "no tools: the build broke before its start task had them; fetching them to end the suite" >&2
  set +e
  (
#BOOTSTRAP#
  )
  rc=$?
  set -e
  if [ "$rc" -ne 0 ] || ! test -x tools/pipelinegen; then
    echo "the tools could not be fetched (exit $rc): the build broke before it could report" >&2
    exit 0
  fi
fi
rc=0
tools/pipelinegen report suite --root trigger --batches batches || rc=$?
case "$rc" in
0) ;;
4) exit 4 ;;
*) echo "the suite's end exited $rc; its statuses say why" >&2 ;;
esac
