if [ -f source/pipelinegen.yaml ]; then
  tools/pipelinegen lock release "$PIPELINEGEN_LOCK" --root source || rc=$?
fi
