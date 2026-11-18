package runstep

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func task(t *testing.T, env ...string) (*Task, *bytes.Buffer) {
	t.Helper()

	var out bytes.Buffer

	return &Task{
		Dir: t.TempDir(), Env: append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, env...),
		Stdout: &out, Stderr: &out, HTTP: &http.Client{}, Now: time.Now,
	}, &out
}

func write(t *testing.T, p, body string, mode os.FileMode) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func fakeTool(t *testing.T, tk *Task, body string) {
	t.Helper()

	write(t, filepath.Join(tk.Dir, tool), "#!/bin/sh\n"+body, 0o700)
}

func TestAStepNamedWrongIsRefused(t *testing.T) {
	for _, args := range [][]string{nil, {"plans"}} {
		tk, out := task(t)
		if code := tk.Run(args); code != 2 || !strings.Contains(out.String(), "step") {
			t.Errorf("%v: exit %d: %s", args, code, out)
		}
	}

	if got := strings.Join(Names(), " "); got != "begin continuous-plan end pipeline plan proposed-plan proposed-write report-error "+
		"stale start sweep sweep-plan sweep-write tools" {
		t.Errorf("steps %s", got)
	}
}

// A variable the step needs and doesn't have stops it as `set -u` did.
func TestAStepWithoutItsVariables(t *testing.T) {
	for _, name := range []string{"plan", "begin", "proposed-write", "sweep", "start"} {
		tk, out := task(t)
		fakeTool(t, tk, "echo called >> called\n")
		write(t, filepath.Join(tk.Dir, "trigger", ".git", "resource", "version.json"), "{}", 0o600)

		if code := tk.Run([]string{name}); code != 2 || !strings.Contains(out.String(), "PIPELINEGEN_RUN: parameter not set\n") {
			t.Errorf("%s: exit %d: %s", name, code, out)
		}

		if _, err := os.Stat(filepath.Join(tk.Dir, "called")); err == nil {
			t.Errorf("%s: called the tool without its run", name)
		}
	}
}

func TestTheContinuousPlanNeedsItsLimit(t *testing.T) {
	for _, args := range [][]string{nil, {"--plan-minutes", "0"}, {"--plan-minutes", "x"}} {
		tk, _ := task(t, "PIPELINEGEN_RUN=r")
		if code := tk.Run(append([]string{"continuous-plan"}, args...)); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}

// The plan's limit counts from the decision's end, and the prepare command
// and the plan see it.
func TestTheContinuousPlansDeadline(t *testing.T) {
	tk, out := task(t, "PIPELINEGEN_RUN=r", "PIPELINEGEN_DEADLINE=1", "PIPELINEGEN_LIMIT=limits.plan=90",
		`PIPELINEGEN_PREPARE=echo "$PIPELINEGEN_DEADLINE $PIPELINEGEN_LIMIT" > ../prepared`)
	at := time.Unix(1_000_000, 0)
	tk.Now = func() time.Time { return at }

	fakeTool(t, tk, "echo \"$2 $PIPELINEGEN_DEADLINE $PIPELINEGEN_LIMIT\" >> calls\n")

	if err := os.MkdirAll(filepath.Join(tk.Dir, "trigger"), 0o750); err != nil {
		t.Fatal(err)
	}

	if code := tk.Run([]string{"continuous-plan", "--plan-minutes", "30"}); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}

	want := "--decide 1 limits.plan=90\n--run 1001800 limits.plan=30\n"
	if got := readFile(t, filepath.Join(tk.Dir, "calls")); got != want {
		t.Errorf("calls %q, want %q", got, want)
	}

	if got := readFile(t, filepath.Join(tk.Dir, "prepared")); got != "1001800 limits.plan=30\n" {
		t.Errorf("the prepare command's limit %q", got)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

// A command's exit reaches the task as a shell gives it.
func TestExitCodes(t *testing.T) {
	for _, c := range []struct {
		body string
		code int
	}{
		{"exit 0\n", 0},
		{"exit 3\n", 3},
		{"exit 4\n", 4},
		{"kill -TERM $$\n", 128 + 15},
	} {
		tk, _ := task(t)
		fakeTool(t, tk, c.body)

		if code := tk.call(tk.Env, tool); code != c.code {
			t.Errorf("%q: exit %d, want %d", c.body, code, c.code)
		}
	}

	tk, out := task(t)
	if code := tk.call(tk.Env, tool); code != 127 || !strings.Contains(out.String(), tool) {
		t.Errorf("a missing tool: exit %d: %s", code, out)
	}

	write(t, filepath.Join(tk.Dir, tool), "#!/bin/sh\n", 0o600)

	if code := tk.call(tk.Env, tool); code != 126 {
		t.Errorf("a tool that can't run: exit %d", code)
	}
}

func TestEnv(t *testing.T) {
	env := []string{"A=1", "B=2", "A=3", "C="}
	if got := getenv(env, "A"); got != "3" {
		t.Errorf("A %q", got)
	}

	if got := with(env, "B=4", "D=5"); !slices.Equal(got, []string{"A=1", "A=3", "C=", "B=4", "D=5"}) {
		t.Errorf("with %v", got)
	}

	if got := unset(env, "A", "C"); !slices.Equal(got, []string{"B=2"}) {
		t.Errorf("unset %v", got)
	}

	tk, _ := task(t, "PIPELINEGEN_PLANNER_ENV=GO_TOKEN GITHUB_TOKEN GOPRIVATE", "GO_TOKEN=x", "GITHUB_TOKEN=y", "GOPRIVATE=z", "OTHER=o")
	if got := tk.withoutPlanner(tk.Env); slices.Contains(got, "GO_TOKEN=x") || slices.Contains(got, "GOPRIVATE=z") ||
		!slices.Contains(got, "GITHUB_TOKEN=y") || !slices.Contains(got, "OTHER=o") {
		t.Errorf("without the planner's %v", got)
	}
}

func TestGitAuthsLines(t *testing.T) {
	got, err := exportsOf("export GIT_CONFIG_COUNT=1\nexport GIT_CONFIG_KEY_0='credential.https://github.com.helper'\n" +
		"export GIT_CONFIG_VALUE_0='!f() { test \"$1\" = get && echo \"password=${GO_TOKEN}\"; }; f'\nexport GIT_TERMINAL_PROMPT=0\n")
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=credential.https://github.com.helper",
		`GIT_CONFIG_VALUE_0=!f() { test "$1" = get && echo "password=${GO_TOKEN}"; }; f`, "GIT_TERMINAL_PROMPT=0",
	}
	if !slices.Equal(got, want) {
		t.Errorf("exports %q", got)
	}

	for _, bad := range []string{"echo hi", "export A=$(id)", "export A='x'y", "A=1"} {
		if _, err := exportsOf(bad); err == nil {
			t.Errorf("%q was read", bad)
		}
	}
}

// git asks the helper the fetch sets up for the read token, from the
// environment; the answer is compared here and never printed.
func TestTheFetchsGitAsksTheReadToken(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}

	empty := filepath.Join(t.TempDir(), "gitconfig")
	write(t, empty, "", 0o600)

	for _, c := range []struct {
		env  []string
		host string
		want string
	}{
		{[]string{"GITHUB_TOKEN=one-token-value"}, "github.com", "one-token-value"},
		{[]string{"GITHUB_TOKEN=one-token-value", "PIPELINEGEN_READ_TOKEN=read-token-value"}, "github.com", "read-token-value"},
		{[]string{"GITHUB_TOKEN=one-token-value"}, "example.com", ""},
	} {
		tk, _ := task(t, append(c.env, "GIT_CONFIG_GLOBAL="+empty, "GIT_CONFIG_NOSYSTEM=1")...)

		cmd := exec.CommandContext(t.Context(), "git", "credential", "fill")
		cmd.Env = tk.fetchEnv(tk.Env)
		cmd.Stdin = strings.NewReader("protocol=https\nhost=" + c.host + "\n\n")

		out, _ := cmd.Output() //nolint:errcheck // a host without a helper fails to fill, and answers nothing

		got := ""
		if m := regexp.MustCompile(`(?m)^password=(.*)$`).FindStringSubmatch(string(out)); m != nil && strings.Contains(string(out), "username=x-access-token\n") {
			got = m[1]
		}

		if got != c.want {
			t.Errorf("%v %s: the helper answered with another token (withheld)", c.env, c.host)
		}
	}
}

func TestTheFetchsGoEnv(t *testing.T) {
	tk, _ := task(t, "PIPELINEGEN_TOOL_REPOSITORY=o/pipelinegen", "PIPELINEGEN_PRIVATE=github.com/o/a,github.com/o/b")
	env := tk.fetchEnv(tk.Env)

	for k, want := range map[string]string{
		"GOPRIVATE":  "github.com/o/pipelinegen,github.com/o/a,github.com/o/b",
		"GOMODCACHE": filepath.Join(tk.Dir, "cache", "gomodcache"),
		"GOCACHE":    filepath.Join(tk.Dir, "cache", "gocache"),
	} {
		if got := getenv(env, k); got != want {
			t.Errorf("%s %q, want %q", k, got, want)
		}
	}

	tk, _ = task(t, "PIPELINEGEN_TOOL_REPOSITORY=o/pipelinegen")
	if got := getenv(tk.fetchEnv(tk.Env), "GOPRIVATE"); got != "github.com/o/pipelinegen" {
		t.Errorf("GOPRIVATE %q", got)
	}
}

func TestPluginPins(t *testing.T) {
	got := pluginPins("go o/go-sbom v1 abc -\n\n  \nts o/ts - - c0ffee\nshort o/short\n" +
		"gd https://github.com/o/plugins v2 def - driver/go\nrc git@gitlab.com:g/plugins.git - - c0ffee runner/concourse\n" +
		"sm o/sums v5 - - - f00d\n")
	want := []toolPin{
		{name: "go", repo: "o/go-sbom", version: "v1", sum: "abc"},
		{name: "ts", repo: "o/ts", commit: "c0ffee"},
		{name: "short", repo: "o/short"},
		{name: "gd", repo: "https://github.com/o/plugins", version: "v2", sum: "def", path: "driver/go"},
		{name: "rc", repo: "git@gitlab.com:g/plugins.git", commit: "c0ffee", path: "runner/concourse"},
		{name: "sm", repo: "o/sums", version: "v5", sums: "f00d"},
	}

	if !slices.Equal(got, want) {
		t.Errorf("pins %+v", got)
	}

	for _, c := range []struct {
		pin          toolPin
		program, tag string
	}{
		{got[0], "go-sbom", "v1"},
		{got[3], "go-driver", "driver/go/v2"},
		{toolPin{repo: "https://github.com/o/pipelinegen-sbom-go.git", version: "v3"}, "go-sbom", "v3"},
	} {
		if p, tag := c.pin.program(), c.pin.tag(); p != c.program || tag != c.tag {
			t.Errorf("%+v: program %s tag %s, want %s %s", c.pin, p, tag, c.program, c.tag)
		}
	}

	for repo, want := range map[string][2]string{
		"o/r":                            {"github.com/o/r", "https://github.com/o/r.git"},
		"https://github.com/o/r":         {"github.com/o/r", "https://github.com/o/r.git"},
		"git@github.com:o/r.git":         {"github.com/o/r", "https://github.com/o/r.git"},
		"https://gitlab.com/g/sub/r.git": {"gitlab.com/g/sub/r", "https://gitlab.com/g/sub/r.git"},
	} {
		if mod, clone := ModulePath(repo), cloneURL(repo); mod != want[0] || clone != want[1] {
			t.Errorf("%s: module %s, clone %s, want %v", repo, mod, clone, want)
		}
	}
}

func TestTheHandOffsCommitted(t *testing.T) {
	re := regexp.MustCompile(`"committed":"[^"]*"`)

	for in, want := range map[string]string{
		`{"commit":"c","committed":"old"}`:               `{"commit":"c","committed":"NOW"}`,
		"{\"committed\":\"a\",\"committed\":\"b\"}\nx":   "{\"committed\":\"NOW\",\"committed\":\"b\"}\nx",
		"{\"committed\":\"a\"}\n{\"committed\":\"b\"}\n": "{\"committed\":\"NOW\"}\n{\"committed\":\"NOW\"}\n",
	} {
		if got := firstOnEachLine(in, re, `"committed":"NOW"`); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}

	tk, out := task(t)
	tk.Now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 3600)) }

	write(t, filepath.Join(tk.Dir, "h", ".git", "resource", "version.json"), "{\"pr\":\"7\"}\n{\"x\":1}", 0o600)

	if code := tk.handoff("h", "o"); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}

	got := readFile(t, filepath.Join(tk.Dir, "o", ".git", "resource", "version.json"))
	if !regexp.MustCompile(`^\{"committed":"2026-01-02T02:04:05\.[0-9]{9}Z","pr":"7"\}` + "\n" + `\{"committed":"2026-01-02T02:04:05\.[0-9]{9}Z","x":1\}$`).MatchString(got) {
		t.Errorf("version %q", got)
	}

	if got := readFile(t, filepath.Join(tk.Dir, "o", ".git", "resource", "metadata.json")); got != "[]\n" {
		t.Errorf("metadata %q", got)
	}

	if code := tk.handoff("missing", "o2"); code != 2 {
		t.Errorf("a head without its version: exit %d", code)
	}
}

func TestStalesWords(t *testing.T) {
	for said, want := range map[string]string{
		"": "",
		"pipelinegen pipeline: runner: exit status 2\n":                              "",
		"before\npipelinegen pipeline: config a.yaml:\n\tline 2:\t\tbad   key  \n\n": "pipelinegen pipeline: config a.yaml: line 2: bad key",
		"pipelinegen pipeline: config " + strings.Repeat("é", 200):                   ("pipelinegen pipeline: config " + strings.Repeat("é", 200))[:300],
	} {
		if got := unreadable(said); got != want {
			t.Errorf("%q: %q, want %q", said, got, want)
		}
	}

	text := "a: 1\n  PIPELINEGEN_PIPELINE_SHA256: abc\n\tPIPELINEGEN_PIPELINE_SHA256: x y\nb: PIPELINEGEN_PIPELINE_SHA256: z\n"
	if got, want := pipelineHash(text), pipelineHash("a: 1\n  PIPELINEGEN_PIPELINE_SHA256: \"\"\n\tPIPELINEGEN_PIPELINE_SHA256: \"\"\nb: PIPELINEGEN_PIPELINE_SHA256: z\n"); got != want {
		t.Errorf("the hash's own line counts: %s %s", got, want)
	}
}

func TestARequestThatFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
		if err != nil {
			t.Error(err)
		}

		w.WriteHeader(code)
	}))
	defer srv.Close()

	for path, want := range map[string]int{"/200": 0, "/302": 0, "/404": 22, "/500": 22} {
		tk, out := task(t)
		client := *tk.HTTP
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+path, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}

		if code := tk.requested(&client, req, nil); code != want {
			t.Errorf("%s: exit %d, want %d: %s", path, code, want, out)
		}
	}

	tk, _ := task(t)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:1/", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}

	if code := tk.requested(tk.HTTP, req, nil); code != 7 {
		t.Errorf("a server that isn't there: exit %d", code)
	}
}

// The runner may place itself over the copy that runs: a rename replaces
// it where a write would find it busy.
func TestPlaceReplacesARunningProgram(t *testing.T) {
	tk, out := task(t)
	f := &fetcher{t: tk}

	write(t, filepath.Join(tk.Dir, "new"), "#!/bin/sh\necho new\n", 0o700)
	write(t, filepath.Join(tk.Dir, "dest"), "#!/bin/sh\nsleep 5\n", 0o700)

	running := exec.CommandContext(t.Context(), filepath.Join(tk.Dir, "dest"))
	if err := running.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := running.Process.Kill(); err != nil {
			t.Log(err)
		}

		_ = running.Wait() //nolint:errcheck // killed
	})

	sum := "3d1df0a0a8b2a0d72c3b7d4a3ba9f2b6c1f7b1a42d5e0c4b2a9b5d8f2c6e1a7b"

	if code := f.place("new", "", "dest"); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}

	if got := readFile(t, filepath.Join(tk.Dir, "dest")); got != "#!/bin/sh\necho new\n" {
		t.Errorf("dest %q", got)
	}

	if code := f.place("new", sum, "dest"); code != 1 || !strings.Contains(out.String(), "dest doesn't match its sha256") {
		t.Errorf("a wrong sum: exit %d: %s", code, out)
	}
}

func TestPruneKeepsHiddenEntries(t *testing.T) {
	tk, out := task(t)
	f := &fetcher{t: tk, pinned: []string{"a-release-v1"}}

	for _, d := range []string{"a-release-v1", "a-release-v0", ".b-release-v1", "c-commit-x", "bazelisk-v1", "gomodcache"} {
		if err := os.MkdirAll(filepath.Join(tk.Dir, "cache", d), 0o750); err != nil {
			t.Fatal(err)
		}
	}

	if code := f.prune(); code != 0 {
		t.Fatalf("exit %d", code)
	}

	if got := out.String(); got != "dropped from the job's cache, as no pin names them: a-release-v0 c-commit-x bazelisk-v1\n" {
		t.Errorf("said %q", got)
	}

	entries, err := os.ReadDir(filepath.Join(tk.Dir, "cache"))
	if err != nil {
		t.Fatal(err)
	}

	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}

	if strings.Join(left, " ") != ".b-release-v1 a-release-v1 gomodcache" {
		t.Errorf("left %v", left)
	}
}

// No step passes a token on in a command's arguments or prints it: each
// reads its token from the environment.
func TestNoStepPassesATokenOn(t *testing.T) {
	tokens := []string{
		"GITHUB_TOKEN=token-one-value", "PIPELINEGEN_READ_TOKEN=token-read-value", "PIPELINEGEN_CHANGES_TOKEN=token-changes-value",
		"PIPELINEGEN_REPORT_TOKEN=token-report-value", "PIPELINEGEN_STORE_TOKEN=token-store-value", "GO_TOKEN=token-planner-value",
	}

	for _, name := range Names() {
		if name == "tools" {
			continue
		}

		tk, out := task(t, append(tokens, "PIPELINEGEN_RUN=r", "PIPELINEGEN_SUITE=s", "PIPELINEGEN_PLANNER_ENV=GO_TOKEN",
			"PIPELINEGEN_GIT_TOKEN=GO_TOKEN", "PIPELINEGEN_PREPARE=echo prepared", "REPOSITORY=o/r", "SUITE_CONTEXT=p/r/s",
			"RUN_CONTEXT=p/r", "TARGET_URL=https://ci/x", "PIPELINEGEN_GITHUB_API=http://127.0.0.1:1")...)
		fakeTool(t, tk, "echo \"$*\" >> calls\necho \"said $*\"\n")

		for _, d := range []string{"trigger/.git/resource", "base", "sweep/plan/7", "sweep/handoff/9/.git/resource", "plans/7", "gate", "batches", "handoffs"} {
			if err := os.MkdirAll(filepath.Join(tk.Dir, d), 0o750); err != nil {
				t.Fatal(err)
			}
		}

		write(t, filepath.Join(tk.Dir, "trigger", ".git", "resource", "version.json"), `{"commit":"abc"}`, 0o600)
		write(t, filepath.Join(tk.Dir, "sweep", "handoff", "9", ".git", "resource", "version.json"), `{"commit":"abc"}`, 0o600)

		args := []string{name}
		if name == "continuous-plan" {
			args = append(args, "--plan-minutes", "60")
		}

		tk.Run(args)

		calls, err := os.ReadFile(filepath.Join(tk.Dir, "calls"))
		if err != nil {
			t.Fatalf("%s: called nothing: %v\n%s", name, err, out)
		}

		if strings.Contains(string(calls), "token-") || strings.Contains(out.String(), "token-") {
			t.Errorf("%s passed a token on (withheld)", name)
		}
	}
}

// Tools the task holds at their pins are kept, with nothing fetched; one
// that doesn't match its pin is fetched again.
func TestTheFetchKeepsPlacedTools(t *testing.T) {
	standIn := "#!/bin/sh\n"
	sum := sha256.Sum256([]byte(standIn))
	hexSum := hex.EncodeToString(sum[:])

	for _, c := range []struct {
		name string
		sum  string
		code int
	}{
		{"placed", hexSum, 0},
		{"another file", strings.Repeat("a", 64), 1},
	} {
		tk, out := task(t, "PIPELINEGEN_GITHUB_API=http://127.0.0.1:1", "PIPELINEGEN_BAZELISK_BASE=http://127.0.0.1:1",
			"PIPELINEGEN_TOOL_REPOSITORY=o/pipelinegen", "PIPELINEGEN_TOOL_COMMIT=c0ffee",
			"PIPELINEGEN_BAZELISK_VERSION=v1", "PIPELINEGEN_BAZELISK_SHA256="+c.sum,
			"PIPELINEGEN_PLUGINS=runner o/pipelinegen-runner-x v1 "+c.sum+" -\ngo o/go-sbom - - c0ffee\n")

		for _, p := range []string{tool, "tools/bazelisk", "tools/plugins/runner", "tools/plugins/go"} {
			write(t, filepath.Join(tk.Dir, p), standIn, 0o700)
		}

		if code := tk.fetchTools(tk.Env); code != c.code {
			t.Errorf("%s: exit %d\n%s", c.name, code, out)
		}
	}
}
