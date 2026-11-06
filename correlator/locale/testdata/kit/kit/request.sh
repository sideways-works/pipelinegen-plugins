# One part of a slice against the stack: a request for a mail in a
# language, answered once the server has looked its keys up.
set -eu
r=coverage/requests/part-$PIPELINEGEN_PART
lang=de
if [ "$PIPELINEGEN_PART" = 2 ]; then lang=en; fi
echo "$lang" > "$r.tmp"
mv "$r.tmp" "$r"
while [ -e "$r" ]; do sleep 0.02; done
