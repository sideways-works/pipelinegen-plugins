# One part of a slice, as a batch task runs a Go test binary built with the
# recorder: the process writes its file into the slice's directory, as
# gorecord does with PIPELINEGEN_LOCALE_KEYS_DIR.
set -eu
d=coverage/$PIPELINEGEN_SLICE
mkdir -p "$d"
f=$d/locale-keys.go-$$-$PIPELINEGEN_PART.jsonl
{
  echo '{"recorder":"locale-keys","version":1,"runtime":"go","pid":'$$'}'
  echo '{"c":"mail/locales","load":true}'
  echo '{"c":"mail/locales","l":"de","n":"","k":"booking.subject"}'
  if [ "$PIPELINEGEN_PART" = 2 ]; then echo '{"c":"mail/locales","l":"de","n":"","k":"booking.lines"}'; fi
} > "$f"
