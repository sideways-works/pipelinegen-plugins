# Reports a suite whose build broke or was aborted. When the build had got
# as far as its tools and its trigger, the tool reports the suite itself,
# its end mark included, so the run's round can end; a suite's end it could
# not record (exit 4) fails this step. Otherwise, or when the tool could
# not report, it posts the suite's status and its run's as errored. Without
# the trigger's checkout the commit is unknown, and there is nothing to
# post to.
set -eu

if test -x tools/pipelinegen && test -d trigger; then
  rc=0
  tools/pipelinegen report suite --root trigger --batches batches || rc=$?
  case "$rc" in
  0|1) exit 0 ;;
  4) exit 4 ;;
  esac
  echo "the suite's end exited $rc: posting its status as errored" >&2
fi

commit=""
if [ -f trigger/.git/resource/version.json ]; then
  commit=$(tr -d '\n ' < trigger/.git/resource/version.json | sed -n 's/.*"commit":"\([0-9a-f]*\)".*/\1/p')
elif [ -d trigger/.git ]; then
  commit=$(git -C trigger rev-parse HEAD)
fi

if [ -z "$commit" ]; then
  echo "no trigger: nothing to post to" >&2
  exit 0
fi

api=${PIPELINEGEN_GITHUB_API:-https://api.github.com}

# curl reads its authorization header from standard input, written by awk
# from the environment, so no argument or variable here holds the token
for context in "$SUITE_CONTEXT" "$RUN_CONTEXT"; do
  awk 'BEGIN { t = ENVIRON["PIPELINEGEN_REPORT_TOKEN"]; if (t == "") t = ENVIRON["GITHUB_TOKEN"]; print "Authorization: Bearer " t }' |
    curl -fsS -X POST -H @- -H "Accept: application/vnd.github+json" \
    "$api/repos/$REPOSITORY/statuses/$commit" \
    -d "{\"state\":\"error\",\"context\":\"$context\",\"description\":\"the build broke before it could report\",\"target_url\":\"$TARGET_URL\"}" >/dev/null
done
