package runstep

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/plugin"
)

var errUnread = errors.New("unreadable")

// fetchTools places the pinned tools in tools/: pipelinegen, the plugins in
// tools/plugins and bazelisk. Each is kept in the job's cache, which is
// checked every time: a wrong binary under a right name never runs.
func (t *Task) fetchTools(env []string) int {
	f := &fetcher{t: t, env: t.fetchEnv(env)}

	for _, d := range []string{"cache", "tools/plugins"} {
		if err := os.MkdirAll(t.path(d), 0o750); err != nil {
			t.sayf("%v", err)

			return 1
		}
	}

	// a list an older bootstrap kept while it ran
	if code := t.remove("cache/.pinned"); code != 0 {
		return code
	}

	if code := f.fetch(pin{
		repo: getenv(env, "PIPELINEGEN_TOOL_REPOSITORY"), version: getenv(env, "PIPELINEGEN_TOOL_VERSION"),
		sum: getenv(env, "PIPELINEGEN_TOOL_SHA256"), commit: getenv(env, "PIPELINEGEN_TOOL_COMMIT"),
	}, tool); code != 0 {
		return code
	}

	for _, p := range pluginPins(getenv(env, "PIPELINEGEN_PLUGINS")) {
		if code := f.fetch(p, filepath.Join("tools/plugins", p.name)); code != 0 {
			return code
		}
	}

	if code := f.bazelisk(getenv(env, "PIPELINEGEN_BAZELISK_VERSION"), getenv(env, "PIPELINEGEN_BAZELISK_SHA256")); code != 0 {
		return code
	}

	return f.prune()
}

// fetchEnv is what git and go see while the tools are fetched: the module
// and build caches stay in the job's cache, so a build from a commit starts
// warm, and git's credential helper reads the token from the environment,
// so no line here holds it. A plugin built from a commit may need the
// tool's module, which may be private.
func (t *Task) fetchEnv(env []string) []string {
	root, err := filepath.Abs(t.Dir)
	if err != nil {
		root = t.Dir
	}

	private := "github.com/" + getenv(env, "PIPELINEGEN_TOOL_REPOSITORY")
	if more := getenv(env, "PIPELINEGEN_PRIVATE"); more != "" {
		private += "," + more
	}

	return with(env,
		"GOMODCACHE="+filepath.Join(root, "cache", "gomodcache"), "GOCACHE="+filepath.Join(root, "cache", "gocache"),
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=credential.https://github.com.helper",
		`GIT_CONFIG_VALUE_0=!f() { test "$1" = get && echo username=x-access-token && echo "password=${PIPELINEGEN_READ_TOKEN:-${GITHUB_TOKEN:-}}"; }; f`,
		"GIT_TERMINAL_PROMPT=0", "GOPRIVATE="+private,
	)
}

type pin struct {
	name, repo, version, sum, commit string
}

// pluginPins reads the plugins' pins, one per line: name, repository,
// version, sha256 and commit, "-" for an empty field.
func pluginPins(lines string) []pin {
	var out []pin

	for line := range strings.SplitSeq(lines, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}

		for len(f) < 5 {
			f = append(f, "")
		}

		for i := 2; i < 5; i++ {
			if f[i] == "-" {
				f[i] = ""
			}
		}

		out = append(out, pin{name: f[0], repo: f[1], version: f[2], sum: f[3], commit: f[4]})
	}

	return out
}

type fetcher struct {
	t   *Task
	env []string
	// pinned are the cache's entries this build's pins name.
	pinned []string
}

// verified says the file is there, executable, and has the checksum.
func (f *fetcher) verified(p, sum string) bool {
	if sum == "" || !f.t.executable(p) {
		return false
	}

	data, err := os.ReadFile(f.t.path(p))
	if err != nil {
		return false
	}

	got := sha256.Sum256(data)

	return hex.EncodeToString(got[:]) == sum
}

// place copies a verified file and checks the copy, the one that runs. The
// copy replaces the file at dest by a rename, so a running program may be
// replaced.
func (f *fetcher) place(from, sum, dest string) int {
	if code := f.copy(from, dest); code != 0 {
		return code
	}

	if sum != "" && !f.verified(dest, sum) {
		f.t.sayf("%s doesn't match its sha256", dest)

		return 1
	}

	return 0
}

func (f *fetcher) copy(from, dest string) int {
	data, err := os.ReadFile(f.t.path(from))
	if err != nil {
		f.t.sayf("cp: %v", err)

		return 1
	}

	tmp := dest + ".tmp"
	if err := os.WriteFile(f.t.path(tmp), data, 0o755); err != nil { //nolint:gosec // an executable
		f.t.sayf("cp: %v", err)

		return 1
	}

	if err := os.Rename(f.t.path(tmp), f.t.path(dest)); err != nil {
		f.t.sayf("cp: %v", err)

		return 1
	}

	return 0
}

// download fetches a file into the cache, keeping it only when it matches
// its checksum.
func (f *fetcher) download(req *http.Request, sum, file, mismatch string) int {
	if code := f.t.remove(file, file+".tmp"); code != 0 {
		return code
	}

	var body bytes.Buffer
	if code := f.t.requested(f.t.HTTP, req, &body); code != 0 {
		return code
	}

	if err := os.WriteFile(f.t.path(file+".tmp"), body.Bytes(), 0o755); err != nil { //nolint:gosec // an executable
		f.t.sayf("%v", err)

		return 1
	}

	if !f.verified(file+".tmp", sum) {
		_ = f.t.remove(file + ".tmp") // the mismatch is what's reported
		f.t.sayf("%s", mismatch)

		return 1
	}

	if err := os.Rename(f.t.path(file+".tmp"), f.t.path(file)); err != nil {
		f.t.sayf("mv: %v", err)

		return 1
	}

	return 0
}

// fetch places a pinned tool at dest: a release's file <program>-linux-amd64,
// else a Node plugin's one file <program>.mjs, run by its node shebang; or
// ./cmd/<program> built at a commit, which has no checksum and is cached by
// its commit.
func (f *fetcher) fetch(p pin, dest string) int {
	name := plugin.Program(p.repo)

	if p.commit != "" {
		return f.build(p, name, dest)
	}

	dir := filepath.Join("cache", name+"-release-"+p.version)
	f.pinned = append(f.pinned, filepath.Base(dir))

	if err := os.MkdirAll(f.t.path(dir), 0o750); err != nil {
		f.t.sayf("%v", err)

		return 1
	}

	cached := filepath.Join(dir, name)

	if !f.verified(cached, p.sum) {
		if code := f.release(p, name, cached); code != 0 {
			return code
		}
	}

	return f.place(cached, p.sum, dest)
}

type asset struct {
	URL  string `json:"url"`
	Name string `json:"name"`
}

// release downloads the release's file into the cache. A release the API
// couldn't list has no files either.
func (f *fetcher) release(p pin, name, cached string) int {
	var assets struct {
		Assets []asset `json:"assets"`
	}

	if req, err := f.request(f.t.api()+"/repos/"+p.repo+"/releases/tags/"+p.version, ""); err == nil {
		var body bytes.Buffer
		if f.t.requested(f.t.HTTP, req, &body) == 0 {
			_ = json.Unmarshal(body.Bytes(), &assets) //nolint:errcheck // an answer that isn't a release lists no files
		}
	}

	file, url := "", ""

	for _, want := range []string{name + "-linux-amd64", name + ".mjs"} {
		if i := slices.IndexFunc(assets.Assets, func(a asset) bool { return a.Name == want && a.URL != "" }); i >= 0 {
			file, url = want, assets.Assets[i].URL

			break
		}
	}

	if url == "" {
		f.t.sayf("release %s of %s has neither %s-linux-amd64 nor %s.mjs", p.version, p.repo, name, name)

		return 1
	}

	req, err := f.request(url, "application/octet-stream")
	if err != nil {
		f.t.sayf("%v", err)

		return 1
	}

	return f.download(req, p.sum, cached, "release "+p.version+" of "+p.repo+": "+file+" doesn't match its sha256")
}

// request asks the API with the read token, taken from the environment.
func (f *fetcher) request(url, accept string) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodGet, url, http.NoBody) //nolint:noctx // a task's limit ends it
	if err != nil {
		return nil, err //nolint:wrapcheck // the URL is in the message
	}

	req.Header.Set("Authorization", f.t.bearer("PIPELINEGEN_READ_TOKEN"))

	if accept != "" {
		req.Header.Set("Accept", accept)
	}

	return req, nil
}

// build builds ./cmd/<program> at the pinned commit, once per commit.
func (f *fetcher) build(p pin, name, dest string) int {
	dir := filepath.Join("cache", name+"-commit-"+p.commit)
	f.pinned = append(f.pinned, filepath.Base(dir))

	if err := os.MkdirAll(f.t.path(dir), 0o750); err != nil {
		f.t.sayf("%v", err)

		return 1
	}

	built := filepath.Join(dir, name)

	if !f.t.executable(built) {
		if code := f.buildAt(p, name, built); code != 0 {
			return code
		}
	}

	return f.copy(built, dest)
}

func (f *fetcher) buildAt(p pin, name, built string) int {
	if code := f.t.remove("src"); code != 0 {
		return code
	}

	src := f.t.path("src")

	for _, args := range [][]string{
		{"init", "-q", src},
		{"-C", src, "fetch", "-q", "--depth", "1", "https://github.com/" + p.repo + ".git", p.commit},
		{"-C", src, "checkout", "-q", "FETCH_HEAD"},
	} {
		if code := f.t.call(f.env, "git", args...); code != 0 {
			return code
		}
	}

	cmd := f.t.command(with(f.env, "CGO_ENABLED=0", "GOFLAGS=-trimpath"), src, "go", "build", "-o",
		filepath.Join("..", built+".tmp"), "./cmd/"+name)
	if code := f.t.exited("go", cmd.Run()); code != 0 {
		return code
	}

	if err := os.Chmod(f.t.path(built+".tmp"), 0o755); err != nil { //nolint:gosec // an executable
		f.t.sayf("%v", err)

		return 1
	}

	if err := os.Rename(f.t.path(built+".tmp"), f.t.path(built)); err != nil {
		f.t.sayf("mv: %v", err)

		return 1
	}

	return 0
}

func (f *fetcher) bazelisk(version, sum string) int {
	cached := filepath.Join("cache", "bazelisk-"+version)
	f.pinned = append(f.pinned, filepath.Base(cached))

	if !f.verified(cached, sum) {
		base := getenv(f.env, "PIPELINEGEN_BAZELISK_BASE")
		if base == "" {
			base = "https://github.com/bazelbuild/bazelisk/releases/download"
		}

		url := base + "/" + version + "/bazelisk-linux-amd64"

		req, err := http.NewRequest(http.MethodGet, url, http.NoBody) //nolint:noctx // a task's limit ends it
		if err != nil {
			f.t.sayf("%v", err)

			return 1
		}

		if code := f.download(req, sum, cached, url+" doesn't match its sha256"); code != 0 {
			return code
		}
	}

	return f.place(cached, sum, "tools/bazelisk")
}

// prune drops a release, a commit's build or a bazelisk no pin of this build
// names: Concourse copies a task's whole cache when the task ends, so each
// one kept would cost every build, and a pin that names it again fetches it
// again.
func (f *fetcher) prune() int {
	var dropped []string

	for _, pattern := range []string{"*-release-*", "*-commit-*", "bazelisk-*"} {
		entries, err := filepath.Glob(filepath.Join(f.t.path("cache"), pattern))
		if err != nil {
			continue
		}

		for _, e := range entries {
			name := filepath.Base(e)
			if strings.HasPrefix(name, ".") || slices.Contains(f.pinned, name) || slices.Contains(dropped, name) {
				continue
			}

			if code := f.t.remove(filepath.Join("cache", name)); code != 0 {
				return code
			}

			dropped = append(dropped, name)
		}
	}

	if len(dropped) > 0 {
		f.t.outf("dropped from the job's cache, as no pin names them: %s", strings.Join(dropped, " "))
	}

	return 0
}
