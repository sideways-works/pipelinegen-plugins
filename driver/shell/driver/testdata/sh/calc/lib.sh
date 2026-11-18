# The calculator the tests exercise, and a note of what ran for a
# recording run.
add() { echo $(($1 + $2)); }
neg() { echo $((0 - $1)); }
ran() { [ -z "${PIPELINEGEN_RECORDING:-}" ] || echo "$1" >>"$PIPELINEGEN_RECORDING/ran"; }
