# pipelinegen: install what the steps need: jq
(
set -eu
if command -v apt-get >/dev/null 2>&1; then pg_m=apt-get pg_want='jq' pg_none=''
elif command -v apk >/dev/null 2>&1; then pg_m=apk pg_want='jq' pg_none=''
elif command -v dnf >/dev/null 2>&1; then pg_m=dnf pg_want='jq' pg_none=''
elif command -v microdnf >/dev/null 2>&1; then pg_m=microdnf pg_want='jq' pg_none=''
elif command -v yum >/dev/null 2>&1; then pg_m=yum pg_want='jq' pg_none=''
else
  echo "pipelinegen: this image has none of the package managers apt-get, apk, dnf, microdnf, yum to install jq" >&2
  exit 1
fi
if [ -n "$pg_none" ]; then
  echo "pipelinegen: these packages name nothing for $pg_m: $pg_none" >&2
  exit 1
fi
pg_has() {
  case $pg_m in
  apt-get) case "$(dpkg-query -W -f '${Status}' "$1" 2>/dev/null)" in *" ok installed") ;; *) return 1 ;; esac ;;
  apk) apk info -e "$1" >/dev/null 2>&1 ;;
  *) rpm -q "$1" >/dev/null 2>&1 ;;
  esac
}
pg_missing=
for pg_p in $pg_want; do
  pg_has "$pg_p" || pg_missing="$pg_missing $pg_p"
done
[ -n "$pg_missing" ] || exit 0
if [ "$(id -u)" != 0 ]; then
  echo "pipelinegen: can't install$pg_missing with $pg_m: the step runs as user $(id -u), not root" >&2
  exit 1
fi
pg_cache=${PIPELINEGEN_PACKAGE_CACHE:-}
case $pg_cache in ''|/*) ;; *) pg_cache=$PWD/$pg_cache ;; esac
echo "pipelinegen: installing$pg_missing with $pg_m" >&2
case $pg_m in
apt-get)
  set -- -q -o Dpkg::Use-Pty=0 -o APT::Keep-Downloaded-Packages=true
  if [ -n "$pg_cache" ]; then
    mkdir -p "$pg_cache/apt/partial"
    set -- "$@" -o "Dir::Cache::Archives=$pg_cache/apt"
  fi
  DEBIAN_FRONTEND=noninteractive apt-get "$@" update >/dev/null
  DEBIAN_FRONTEND=noninteractive apt-get "$@" install -y --no-install-recommends $pg_missing >/dev/null
  if [ -n "${PIPELINEGEN_PACKAGE_CLEAN:-}" ]; then apt-get clean; rm -rf /var/lib/apt/lists/*; fi
  ;;
apk)
  if [ -n "$pg_cache" ]; then
    mkdir -p "$pg_cache/apk"
    apk add --no-progress -q --cache-dir "$pg_cache/apk" --update-cache $pg_missing
  else
    apk add --no-progress -q --no-cache $pg_missing
  fi
  ;;
*)
  set -- -y
  [ -z "$pg_cache" ] || set -- "$@" --setopt=keepcache=1 "--setopt=cachedir=$pg_cache/$pg_m"
  $pg_m "$@" install $pg_missing >/dev/null
  [ -z "${PIPELINEGEN_PACKAGE_CLEAN:-}" ] || $pg_m clean all >/dev/null
  ;;
esac
for pg_p in $pg_missing; do
  pg_has "$pg_p" || { echo "pipelinegen: $pg_m installed no $pg_p" >&2; exit 1; }
done
)
