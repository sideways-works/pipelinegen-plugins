# A proposed change's second step: it runs none of the change's code. It
# writes the plan the first step left once it is the change's own, and
# shows the run. The hand-off, written first, is put only when the step
# opens its gate: when it wrote the plan, or found the head planned and
# stalled as the sweep would hand it on again; never for a head whose
# suites began or are on their way.
set -eu

handoff trigger handoff

exec tools/pipelinegen plan --run "$PIPELINEGEN_RUN" --trigger trigger --root trigger --cache write-cache --from plan --gate handoff
