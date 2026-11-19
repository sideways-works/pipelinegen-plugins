set -eu

# The one script a task runs: it places the runner the pipeline pins in
# tools/plugins, checked by its sha256 as every pinned file is, and runs the
# task's step with it. The runner fetches the other tools itself.
runner=tools/plugins/$PIPELINEGEN_RUNNER

# The end runs on an abort too, when the build may have broken before it
# had anything to end with. One that fetches its own tools fails when it
# can't: its suite would never end.
if [ "${1:-}" = end ]; then
  if [ ! -d trigger ]; then
    echo "no trigger: the build broke before it could report" >&2
    exit 0
  fi
  if [ -z "${PIPELINEGEN_OWN_TOOLS:-}" ]; then
    trap 'rc=$?; if [ "$rc" -ne 0 ]; then echo "the tools could not be fetched (exit $rc): the build broke before it could report" >&2; exit 0; fi' EXIT
  fi
fi

# The error hooks post the suite's and the run's statuses as errored when
# the runner can't be had either, so a status never stays pending.
errored() {
  echo "the runner could not be fetched (exit $1): posting the statuses as errored" >&2
  commit=
  if [ -f trigger/.git/resource/version.json ]; then
    commit=$(tr -d '\n ' < trigger/.git/resource/version.json | sed -n 's/.*"commit":"\([0-9a-f]*\)".*/\1/p')
  elif [ -d trigger/.git ]; then
    commit=$(git -C trigger rev-parse HEAD)
  fi
  if [ -z "$commit" ]; then
    echo "no trigger: nothing to post to" >&2
    exit 0
  fi
  # curl reads its authorization header from awk, which reads the token
  # from the environment
  for context in "$SUITE_CONTEXT" "$RUN_CONTEXT"; do
    awk 'BEGIN { t = ENVIRON["PIPELINEGEN_REPORT_TOKEN"]; if (t == "") t = ENVIRON["GITHUB_TOKEN"]; print "Authorization: Bearer " t }' |
      curl -fsS -X POST -H @- -H "Accept: application/vnd.github+json" \
        "${PIPELINEGEN_GITHUB_API:-https://api.github.com}/repos/$REPOSITORY/statuses/$commit" \
        -d "{\"state\":\"error\",\"context\":\"$context\",\"description\":\"the build broke before it could report\",\"target_url\":\"$TARGET_URL\"}" >/dev/null
  done
  exit 0
}
if [ "${1:-}" = report-error ]; then
  trap 'rc=$?; if [ "$rc" -ne 0 ]; then errored "$rc"; fi' EXIT
fi

(
  # the planner's params and secrets are the prepare command's alone
  for name in ${PIPELINEGEN_PLANNER_ENV:-}; do
    [ "$name" = GITHUB_TOKEN ] || unset "$name"
  done
  p=$PIPELINEGEN_RUNNER_PROGRAM v=$PIPELINEGEN_RUNNER_VERSION c=$PIPELINEGEN_RUNNER_COMMIT
  repo=$PIPELINEGEN_RUNNER_REPOSITORY
  # a runner in a directory of its repository is tagged and built there
  tag=${PIPELINEGEN_RUNNER_PATH:+$PIPELINEGEN_RUNNER_PATH/}$v
  if [ -z "$v$c" ]; then
    echo "the pipeline pins no runner: its config's plugins have none of kind runner" >&2
    exit 1
  fi
  sum=$PIPELINEGEN_RUNNER_SHA256
  ok() { [ -x "$1" ] && echo "$sum  $1" | sha256sum -c - >/dev/null 2>&1; }
  # curl reads its authorization header from awk, which reads the token
  # from the environment: no argument or variable here holds it
  auth() { awk 'BEGIN { t = ENVIRON["PIPELINEGEN_READ_TOKEN"]; if (t == "") t = ENVIRON["GITHUB_TOKEN"]; print "Authorization: Bearer " t }'; }
  # asset downloads the release's file $1 to $2
  asset() {
    url=$(auth | curl -fsSL -H @- "${PIPELINEGEN_GITHUB_API:-https://api.github.com}/repos/$repo/releases/tags/$tag" | tr -d '\n' |
      grep -o "\"url\": *\"[^\"]*/releases/assets/[0-9]*\"[^{}]*\"name\": *\"$1\"" | head -n 1 | sed 's/^"url": *"\([^"]*\)".*/\1/')
    if [ -z "$url" ]; then
      echo "release $tag of $repo has no $1" >&2
      exit 1
    fi
    auth | curl -fsSL -H @- -H "Accept: application/octet-stream" -o "$2" "$url"
  }

  # A pin by sums checks the release's SHA256SUMS, whose line checks the runner.
  sums=
  if [ -z "$c" ] && [ -n "${PIPELINEGEN_RUNNER_SUMS:-}" ]; then
    listed() { echo "$PIPELINEGEN_RUNNER_SUMS  $1" | sha256sum -c - >/dev/null 2>&1; }
    sums=$runner.SHA256SUMS
    if ! listed "$sums"; then
      sums=cache/$p-release-$v/SHA256SUMS
      if ! listed "$sums"; then
        mkdir -p "cache/$p-release-$v"
        asset SHA256SUMS "$sums.tmp"
        if ! listed "$sums.tmp"; then
          rm -f "$sums.tmp"
          echo "release $tag of $repo: SHA256SUMS doesn't match the pin's sums" >&2
          exit 1
        fi
        mv "$sums.tmp" "$sums"
      fi
    fi
    sum=$(sed -n "s/^\([0-9a-fA-F]\{64\}\) [ *]*$p-linux-amd64\$/\1/p" "$sums" | head -n 1)
    if [ -z "$sum" ]; then
      echo "release $tag of $repo: its SHA256SUMS lists no $p-linux-amd64" >&2
      exit 1
    fi
  fi

  # A commit's build has no checksum: one this build placed is the one it built.
  if [ -x "$runner" ] && { [ -n "$c" ] || ok "$runner"; }; then
    exit 0
  fi
  mkdir -p cache tools/plugins

  if [ -n "$c" ]; then
    dir=cache/$p-commit-$c
    if [ ! -x "$dir/$p" ]; then
      rm -rf src
      git init -q src
      # git's credential helper reads the token from the environment
      GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=credential.https://github.com.helper GIT_TERMINAL_PROMPT=0 \
        GIT_CONFIG_VALUE_0='!f() { test "$1" = get && echo username=x-access-token && echo "password=${PIPELINEGEN_READ_TOKEN:-${GITHUB_TOKEN:-}}"; }; f' \
        git -C src fetch -q --depth 1 "https://github.com/$repo.git" "$c"
      git -C src checkout -q FETCH_HEAD
      mkdir -p "$dir"
      top=$PWD
      (cd "src/${PIPELINEGEN_RUNNER_PATH:-.}" && CGO_ENABLED=0 GOFLAGS=-trimpath GOWORK=off GOMODCACHE="$top/cache/gomodcache" GOCACHE="$top/cache/gocache" go build -o "$top/$dir/$p" "./cmd/$p")
    fi
    cp "$dir/$p" "$runner"
    exit 0
  fi

  dir=cache/$p-release-$v
  if ! ok "$dir/$p"; then
    mkdir -p "$dir"
    asset "$p-linux-amd64" "$dir/$p.tmp"
    chmod +x "$dir/$p.tmp"
    if ! ok "$dir/$p.tmp"; then
      rm -f "$dir/$p.tmp"
      echo "release $tag of $repo: $p-linux-amd64 doesn't match ${sums:+its line in SHA256SUMS}${sums:-its sha256}" >&2
      exit 1
    fi
    mv "$dir/$p.tmp" "$dir/$p"
  fi
  cp "$dir/$p" "$runner"
  if ! ok "$runner"; then
    echo "$runner doesn't match its sha256" >&2
    exit 1
  fi
  # pipelinegen checks a plugin pinned by sums by the list beside it
  if [ -n "$sums" ] && [ "$sums" != "$runner.SHA256SUMS" ]; then
    cp "$sums" "$runner.SHA256SUMS"
  fi
)

trap - EXIT
exec "$runner" step "$@"
