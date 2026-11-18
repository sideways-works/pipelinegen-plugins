# One step, unlike a proposed change's plan: a branch's code is trusted.
set -eu

prepare_in trigger

# the plan itself sees none of the prepare command's params or secrets
without_planner_env

exec tools/pipelinegen plan --run "$PIPELINEGEN_RUN" --trigger trigger --root trigger --cache plan-cache
