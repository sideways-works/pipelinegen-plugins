# Fetches pipelinegen, bazelisk and the plugins into tools/, keeping them in
# cache/. Each is a release checked against its sha256, or, before the first
# release, built from a commit. The cache keeps only what the pins name.
set -eu

mkdir -p cache tools/plugins
# cache/.pinned lists the cache's entries this build's pins name, one per
# line, as each is fetched
: > cache/.pinned
api=${PIPELINEGEN_GITHUB_API:-https://api.github.com}
# the module and build caches stay in the job's cache, so a build from a
# commit starts warm
export GOMODCACHE="$PWD/cache/gomodcache" GOCACHE="$PWD/cache/gocache"
# git asks a credential helper for github.com, which reads the token from
# the environment when git needs it, the way `pipelinegen git-auth` sets it
# up (pipelinegen isn't fetched yet): these lines name the variable and
# never hold the token
# git-auth:begin
export GIT_CONFIG_COUNT=1
export GIT_CONFIG_KEY_0='credential.https://github.com.helper'
export GIT_CONFIG_VALUE_0='!f() { test "$1" = get && echo username=x-access-token && echo "password=${PIPELINEGEN_READ_TOKEN:-${GITHUB_TOKEN:-}}"; }; f'
export GIT_TERMINAL_PROMPT=0
# git-auth:end

# auth writes curl's authorization header, for `curl -H @-`: awk reads the
# token from the environment, so no argument or variable here holds it. The
# read token when the tokens are split by purpose, else the one token
auth() {
  awk 'BEGIN { t = ENVIRON["PIPELINEGEN_READ_TOKEN"]; if (t == "") t = ENVIRON["GITHUB_TOKEN"]; print "Authorization: Bearer " t }'
}
# a plugin built from a commit may need the tool's module, which may be private
export GOPRIVATE="github.com/$PIPELINEGEN_TOOL_REPOSITORY${PIPELINEGEN_PRIVATE:+,$PIPELINEGEN_PRIVATE}"

# verified <file> <sha256>: the file is there, executable, and has the
# checksum. A cache is checked every time: a wrong binary under a right name
# never runs.
verified() {
  [ -n "$2" ] && [ -x "$1" ] && echo "$2  $1" | sha256sum -c - >/dev/null 2>&1
}

# place <file> <sha256> <dest>: copies a verified file and checks the copy,
# the one that runs.
place() {
  cp "$1" "$3"
  if ! verified "$3" "$2"; then
    echo "$3 doesn't match its sha256" >&2
    exit 1
  fi
}

# download <url> <sha256> <file>: fetches a release file into the cache,
# keeping it only when it matches its checksum.
download() {
  rm -f "$3" "$3.tmp"
  curl -fsSL -o "$3.tmp" "$1"
  chmod +x "$3.tmp"
  if ! verified "$3.tmp" "$2"; then
    rm -f "$3.tmp"
    echo "$1 doesn't match its sha256" >&2
    exit 1
  fi
  mv "$3.tmp" "$3"
}

# asset <release json> <name pattern>: the API URL of the release's asset
# whose name matches, or nothing.
asset() {
  printf '%s' "$1" | grep -o "\"url\": *\"[^\"]*/releases/assets/[0-9]*\"[^{}]*\"name\": *\"$2\"" |
    head -n 1 | sed 's/^"url": *"\([^"]*\)".*/\1/'
}

# kinds are the plugin kinds a repository's name may carry, as the tool's
# plugin.Kinds lists them; a test holds the two equal
kinds="sbom correlator runner driver connector"

# program <repository>: the program the repository holds, which names its
# release files, its ./cmd/<program> and its cache entries: NAME-KIND for a
# repository pipelinegen-KIND-NAME, KIND a plugin kind, else the
# repository's own name. The two spellings of a renamed repository
# (owner/go-sbom, owner/pipelinegen-sbom-go) so fetch and keep the same
# files, as the tool's plugin.Program names them.
program() {
  prog_name=${1##*/}
  for prog_kind in $kinds; do
    case $prog_name in pipelinegen-"$prog_kind"-?*)
      echo "${prog_name#pipelinegen-"$prog_kind"-}-$prog_kind"
      return
      ;;
    esac
  done
  echo "$prog_name"
}

# fetch <repository> <version> <sha256> <commit> <dest>: the executable of
# the program <name> the repository holds, from its release asset
# <name>-linux-amd64 or built from ./cmd/<name> at the commit. A release
# without that asset may be a Node plugin's, whose one asset is the script
# <name>.mjs, run by its own node shebang. A release is checked against its
# sha256 in the cache, fetched again when it doesn't match, and checked
# where it runs; a build from a commit has no checksum and is kept by its
# commit.
fetch() {
  repo=$1 version=$2 sum=$3 commit=$4 dest=$5
  name=$(program "$repo")
  if [ -n "$commit" ]; then
    dir="cache/$name-commit-$commit"
    echo "${dir#cache/}" >> cache/.pinned
    mkdir -p "$dir"
    if [ ! -x "$dir/$name" ]; then
      rm -rf src
      git init -q src
      git -C src fetch -q --depth 1 "https://github.com/$repo.git" "$commit"
      git -C src checkout -q FETCH_HEAD
      (cd src && CGO_ENABLED=0 GOFLAGS=-trimpath go build -o "../$dir/$name.tmp" "./cmd/$name")
      chmod +x "$dir/$name.tmp"
      mv "$dir/$name.tmp" "$dir/$name"
    fi
    cp "$dir/$name" "$dest"
    return
  fi
  dir="cache/$name-release-$version"
  echo "${dir#cache/}" >> cache/.pinned
  mkdir -p "$dir"
  if ! verified "$dir/$name" "$sum"; then
    release=$(auth | curl -fsSL -H @- "$api/repos/$repo/releases/tags/$version" | tr -d '\n')
    file=$name-linux-amd64
    url=$(asset "$release" "$name-linux-amd64")
    if [ -z "$url" ]; then
      file=$name.mjs
      url=$(asset "$release" "$name\\.mjs")
    fi
    if [ -z "$url" ]; then
      echo "release $version of $repo has neither $name-linux-amd64 nor $name.mjs" >&2
      exit 1
    fi
    rm -f "$dir/$name" "$dir/$name.tmp"
    auth | curl -fsSL -H @- -H "Accept: application/octet-stream" -o "$dir/$name.tmp" "$url"
    chmod +x "$dir/$name.tmp"
    if ! verified "$dir/$name.tmp" "$sum"; then
      rm -f "$dir/$name.tmp"
      echo "release $version of $repo: $file doesn't match its sha256" >&2
      exit 1
    fi
    mv "$dir/$name.tmp" "$dir/$name"
  fi
  place "$dir/$name" "$sum" "$dest"
}

fetch "$PIPELINEGEN_TOOL_REPOSITORY" "$PIPELINEGEN_TOOL_VERSION" "$PIPELINEGEN_TOOL_SHA256" \
  "$PIPELINEGEN_TOOL_COMMIT" tools/pipelinegen

# one plugin per line: name, repository, version, sha256, commit ("-" for none)
printf '%s\n' "${PIPELINEGEN_PLUGINS:-}" | while read -r pname prepo pversion psum pcommit; do
  [ -n "$pname" ] || continue
  [ "$pversion" = - ] && pversion=
  [ "$psum" = - ] && psum=
  [ "$pcommit" = - ] && pcommit=
  fetch "$prepo" "$pversion" "$psum" "$pcommit" "tools/plugins/$pname"
done

bazelisk="cache/bazelisk-$PIPELINEGEN_BAZELISK_VERSION"
echo "${bazelisk#cache/}" >> cache/.pinned
if ! verified "$bazelisk" "$PIPELINEGEN_BAZELISK_SHA256"; then
  download "${PIPELINEGEN_BAZELISK_BASE:-https://github.com/bazelbuild/bazelisk/releases/download}/$PIPELINEGEN_BAZELISK_VERSION/bazelisk-linux-amd64" \
    "$PIPELINEGEN_BAZELISK_SHA256" "$bazelisk"
fi
place "$bazelisk" "$PIPELINEGEN_BAZELISK_SHA256" tools/bazelisk

# a release, a commit's build or a bazelisk no pin of this build names goes:
# Concourse copies a task's whole cache when the task ends, so each one kept
# would cost every build, and a pin that names it again fetches it again
dropped=
for entry in cache/*-release-* cache/*-commit-* cache/bazelisk-*; do
  [ -e "$entry" ] || continue
  if ! grep -qxF "${entry#cache/}" cache/.pinned; then
    rm -rf "$entry"
    dropped="$dropped ${entry#cache/}"
  fi
done
rm -f cache/.pinned
[ -z "$dropped" ] || echo "dropped from the job's cache, as no pin names them:$dropped"
