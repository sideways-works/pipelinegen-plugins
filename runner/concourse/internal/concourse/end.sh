# The suite job's last task, in its ensure: it runs whatever the build did.
# A build that broke before its tools or its trigger were there has nothing
# to report (its on_error posted that), and ends here.
# It reports the suite from the trigger. A suite's end that could not be
# recorded (exit 4) fails the job; any other failure here is said and
# passes, since the suite's own statuses show it.
set -eu
if ! test -x tools/pipelinegen || ! test -d trigger; then
  echo "no tools or no trigger: the build broke before it could report" >&2
  exit 0
fi
rc=0
tools/pipelinegen report suite --root trigger --batches batches || rc=$?
case "$rc" in
0) ;;
4) exit 4 ;;
*) echo "the suite's end exited $rc; its statuses say why" >&2 ;;
esac
