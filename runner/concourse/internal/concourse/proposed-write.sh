# Runs none of the change's code. The hand-off is put only when the gate
# opens: the plan was written, or the head was planned and stalled; never
# for a head whose suites began or are on their way.
set -eu

handoff trigger handoff

exec tools/pipelinegen plan --run "$PIPELINEGEN_RUN" --trigger trigger --root trigger --cache write-cache --from plan --gate handoff
