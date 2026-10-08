# Plans a continuous run's commit. First the decision, which plans nothing:
# a build whose commit isn't the branch's newest, or had its round, closes
# the hand-off and ends here, without the prepare; one whose commit is the
# newest waits there while a round of the run is in progress. Only a build
# that goes on runs the repository's prepare command, then the plan, which
# decides once more. The decision has the task's whole time limit, the
# wait's and the plan's; the prepare and the plan have the plan's limit,
# counted from the decision's end, so what the prepare leaves for the plan
# need only last that long.
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
