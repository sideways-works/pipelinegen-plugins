# The batch's stack: one long-running server built with the recorder, which
# loads the catalog when it starts and appends to the log the stack shares
# (PIPELINEGEN_LOCALE_KEYS_LOG), which collect cuts per slice.
set -u
log=coverage/locale-keys.log
echo '{"recorder":"locale-keys","version":1,"runtime":"go","pid":'$$'}' >> "$log"
echo '{"c":"mail/locales","load":true}' >> "$log"
mkdir -p coverage/requests
touch coverage/locale-keys.ready
trap 'exit 0' TERM
while :; do
  for r in coverage/requests/*; do
    [ -e "$r" ] || continue
    # a request renders one mail: the server looks up its keys
    echo '{"c":"mail/locales","l":"'"$(cat "$r")"'","n":"","k":"booking.subject"}' >> "$log"
    rm -f "$r"
  done
  sleep 0.05
done
