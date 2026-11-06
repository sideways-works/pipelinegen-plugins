# check-build.sh <dist>...: fails when a production build holds the locale
# recorder's hook (the function's name or the flag's), which only a test
# build with VITE_PIPELINEGEN_LOCALES may hold.
set -eu
found=$(grep -rlE '__pipelinegenLocales|VITE_PIPELINEGEN_LOCALES' "$@" || true)
if [ -n "$found" ]; then
  echo "a production build holds the locale recorder's hook:" >&2
  echo "$found" >&2
  exit 1
fi
