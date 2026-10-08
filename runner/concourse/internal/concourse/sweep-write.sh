# The sweep's last step runs none of the changes' code: it writes each plan
# the second step left, once it is its head's own, and hands off each head
# whose plan step opened its gate (it wrote the plan) and each planned
# head whose suites never began. Each hand-off becomes one put.
set -eu

list=""

for dir in plans/*; do
  [ -d "$dir" ] || continue
  pr="${dir##*/}"
  rm -rf sweep/head plans/head
  mv "sweep/plan/$pr" sweep/head
  mv "$dir" plans/head
  rm -rf handoffs/.gate
  # a shut gate: planned meanwhile by its planning job, or left out
  if ! tools/pipelinegen plan --run "$PIPELINEGEN_RUN" --trigger sweep/head --root sweep/head --cache write-cache --from plans/head --gate handoffs/.gate; then
    echo "the sweep: pull request $pr: its plan isn't written" >&2
  elif grep -q '"1"' handoffs/.gate/gate.json; then
    handoff sweep/head "handoffs/$pr"
    list="$list,\"$pr\""
  fi
done

rm -rf handoffs/.gate

for dir in sweep/handoff/*; do
  [ -d "$dir" ] || continue
  pr="${dir##*/}"
  handoff "$dir" "handoffs/$pr"
  list="$list,\"$pr\""
done

printf '[%s]\n' "${list#,}" > handoffs/list.json
echo "the sweep hands off [${list#,}]"
