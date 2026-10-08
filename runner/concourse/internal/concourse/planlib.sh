# prepare_in runs the repository's prepare command in a checkout: it fills
# the caches the plugins share (in the job's cache, where they stay) and
# may use the network; the plugins never do. The planner's params and
# secrets are the prepare command's alone, and the pipeline's token
# reaches it only when the planner declares it.
prepare_in() {
  [ -n "${PIPELINEGEN_PREPARE:-}" ] || return 0
  caches="$PWD/plan-cache/caches"
  mkdir -p "$caches/gomodcache" "$caches/gocache" "$caches/npm"
  (
    case " ${PIPELINEGEN_PLANNER_ENV:-} " in
      *" GITHUB_TOKEN "*) ;;
      *) unset GITHUB_TOKEN ;;
    esac
    # pipelinegen's own tokens, split by purpose, are never the command's
    unset PIPELINEGEN_READ_TOKEN PIPELINEGEN_CHANGES_TOKEN PIPELINEGEN_REPORT_TOKEN PIPELINEGEN_STORE_TOKEN
    # git asks a credential helper that reads the declared secret when it
    # needs it: the lines name the variable and never hold the token
    if [ -n "${PIPELINEGEN_GIT_TOKEN:-}" ]; then
      eval "$(tools/pipelinegen git-auth "$PIPELINEGEN_GIT_TOKEN")"
    fi
    cd "$1" || exit 1
    export GOMODCACHE="$caches/gomodcache" GOCACHE="$caches/gocache" npm_config_cache="$caches/npm"
    sh -ec "$PIPELINEGEN_PREPARE"
  )
}

# without_planner_env drops the prepare command's params and secrets, which
# the plan never sees.
without_planner_env() {
  for name in ${PIPELINEGEN_PLANNER_ENV:-}; do
    [ "$name" = GITHUB_TOKEN ] || unset "$name"
  done
}

# handoff writes what the pull request resource's put reads into $2: the
# version and metadata of the head checked out in $1, its "committed" now
# this moment with a random fraction of a second, so a head handed on
# again is a new version. The job puts it only when the plan step's gate
# opened.
handoff() {
  from="$1/.git/resource"
  to="$2/.git/resource"
  mkdir -p "$to"
  now="$(date -u +%Y-%m-%dT%H:%M:%S).$(od -An -N4 -tu4 /dev/urandom | awk '{printf "%09d", $1 % 1000000000}')Z"
  if grep -q '"committed":' "$from/version.json"; then
    sed "s/\"committed\":\"[^\"]*\"/\"committed\":\"$now\"/" "$from/version.json" > "$to/version.json"
  else
    sed "s/^{/{\"committed\":\"$now\",/" "$from/version.json" > "$to/version.json"
  fi
  if [ -f "$from/metadata.json" ]; then
    cp "$from/metadata.json" "$to/metadata.json"
  else
    echo '[]' > "$to/metadata.json"
  fi
}
