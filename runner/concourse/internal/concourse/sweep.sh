# The sweep's first step, which runs none of the changes' code: it lists
# the open heads of a pull request run that the planning job missed (the
# pull requests' check can order a new head behind the ones already
# planned), and the planned heads whose suites never began, each once it
# waited the run's missed_after. The gate opens when there is anything to
# plan or hand off.
set -eu

tools/pipelinegen unplanned --run "$PIPELINEGEN_RUN" --root config --out sweep --cache write-cache

if [ -n "$(ls sweep/plan sweep/handoff 2>/dev/null)" ]; then
  echo '["1"]' > gate/gate.json
else
  echo '[]' > gate/gate.json
fi
