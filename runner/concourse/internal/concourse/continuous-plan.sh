# The decision plans nothing: a commit that isn't the branch's newest, or had
# its round, ends here, and the newest waits while a round is in progress. It
# has the task's whole limit; the prepare and the plan share the plan's limit,
# counted from the decision's end.
set -eu

(
  # the decision sees none of the prepare command's params or secrets
  without_planner_env
  exec tools/pipelinegen plan --decide --run "$PIPELINEGEN_RUN" --trigger trigger --root trigger --cache plan-cache --gate handoff
)

if [ -e handoff/gate.json ]; then
  exit 0
fi

export PIPELINEGEN_DEADLINE=$(( $(date +%s) + #PLAN_SECONDS# )) PIPELINEGEN_LIMIT=#PLAN_LIMIT#

prepare_in trigger

# the plan itself sees none of the prepare command's params or secrets
without_planner_env

exec tools/pipelinegen plan --run "$PIPELINEGEN_RUN" --trigger trigger --root trigger --cache plan-cache --gate handoff
