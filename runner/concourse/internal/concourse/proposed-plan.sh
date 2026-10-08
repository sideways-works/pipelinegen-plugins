# A proposed change's first step: it runs the change's code (Bazel reads
# its build files) and so holds no credential that writes. The prepare
# command runs in the base's tree, never the change's: a change's own
# scripts never run here. The plan goes into plan/ for the next step.
set -eu

prepare_in base

# the plan itself sees none of the prepare command's params or secrets
without_planner_env

exec tools/pipelinegen plan --run "$PIPELINEGEN_RUN" --trigger trigger --root trigger --cache plan-cache --out plan
