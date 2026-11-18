# Nothing here starts Bazel.
set -eu

tools/pipelinegen tree --run "$PIPELINEGEN_RUN" --suite "$PIPELINEGEN_SUITE" --trigger trigger --out source --gate gate

if ! grep -q '"1"' gate/gate.json; then
  echo '[]' > batches/batches.json
  exit 0
fi

exec tools/pipelinegen batches --run "$PIPELINEGEN_RUN" --suite "$PIPELINEGEN_SUITE" --trigger trigger --out batches --root source
