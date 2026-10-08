# A proposed change's planning build begins here, in a step that runs none
# of the change's code: it records that the build plans its head, so the
# run's sweep leaves the head to this build for the plan's limit instead of
# planning it beside a slow plan. A store it can't write only warns.
set -eu

exec tools/pipelinegen plan --begin --run "$PIPELINEGEN_RUN" --trigger trigger --root trigger --cache write-cache
