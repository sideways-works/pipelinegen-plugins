# Runs none of the changes' code. The planning job misses a head the pull
# requests' check orders behind ones already planned; the sweep plans it,
# and hands on planned heads whose suites never began after missed_after.
set -eu

tools/pipelinegen unplanned --run "$PIPELINEGEN_RUN" --root config --out sweep --cache write-cache

if [ -n "$(ls sweep/plan sweep/handoff 2>/dev/null)" ]; then
  echo '["1"]' > gate/gate.json
else
  echo '[]' > gate/gate.json
fi
