# Never prints the pipeline or a diff, which would show its variables'
# values. A config the pinned tool can't read means a stale pipeline too.
set -eu

stale() {
  echo "the pipeline is stale: set it from the default branch with" >&2
  echo "  pipelinegen pipeline --out pipeline.yml && fly -t <target> set-pipeline -p $PIPELINEGEN_PIPELINE -c pipeline.yml" >&2
}

if ! (exec 2>pipeline.err; tools/pipelinegen pipeline --root repo --plugins tools/plugins --gate newest --out pipeline.yml); then
  why=$(sed -n '/^pipelinegen pipeline: config /,$p' pipeline.err | tr '\n\t' '  ' | tr -s ' ' | sed 's/ *$//' | cut -c1-300)
  if [ -z "$why" ]; then
    cat pipeline.err >&2
    exit 1
  fi
  stale
  echo "the pinned tool cannot read this tree's config: $why" >&2
  exit 1
fi

# a build of a config the default branch no longer has checks nothing: the
# newer config's build checks the pipeline
if ! grep -q '"1"' newest/gate.json; then
  exit 0
fi

sum=$(sed 's/^\([[:space:]]*PIPELINEGEN_PIPELINE_SHA256:\).*/\1 ""/' pipeline.yml | sha256sum | cut -d' ' -f1)

if [ "$sum" != "$PIPELINEGEN_PIPELINE_SHA256" ]; then
  stale
  exit 1
fi

echo "the pipeline is current"
