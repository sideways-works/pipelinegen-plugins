# Runs the change's code (Bazel reads its build files), so it holds no
# credential that writes; the prepare command runs the base's scripts, never
# the change's.
set -eu

prepare_in base

# the plan itself sees none of the prepare command's params or secrets
without_planner_env

exec tools/pipelinegen plan --run "$PIPELINEGEN_RUN" --trigger trigger --root trigger --cache plan-cache --out plan
