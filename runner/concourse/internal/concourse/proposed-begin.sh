# Runs none of the change's code. The record makes the sweep leave the head
# to this build for the plan's limit instead of planning it beside a slow
# plan.
set -eu

exec tools/pipelinegen plan --begin --run "$PIPELINEGEN_RUN" --trigger trigger --root trigger --cache write-cache
