# Runs the changes' code, so it holds no credential that writes; the prepare
# command runs the base's scripts.
set -eu

for dir in sweep/plan/*; do
  [ -d "$dir" ] || continue
  pr="${dir##*/}"
  rm -rf sweep/head plans/head
  mv "$dir" sweep/head
  # a head that can't be planned leaves the others theirs; the next sweep
  # tries it again
  if prepare_in base && (without_planner_env && tools/pipelinegen plan --run "$PIPELINEGEN_RUN" --trigger sweep/head --root sweep/head --cache plan-cache --out plans/head); then
    mv plans/head "plans/$pr"
  else
    echo "the sweep: pull request $pr: not planned" >&2
  fi
done
