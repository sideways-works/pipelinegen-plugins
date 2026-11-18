package concourse

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeRunner stands in for the runner: it notes how it was run and with
// what environment, and exits as RUNNER_EXIT says.
const fakeRunner = "#!/bin/sh\necho \"$*\" > ran\nenv > runner.env\nexit \"${RUNNER_EXIT:-0}\"\n"

type runnerReleases struct {
	srv  *httptest.Server
	mu   sync.Mutex
	asks []string
	// body is the runner's release file; status, when set, answers every
	// request.
	body   []byte
	status int
}

func newRunnerReleases(t *testing.T, body []byte) *runnerReleases {
	t.Helper()

	r := &runnerReleases{body: body}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.asks = append(r.asks, req.URL.Path+" "+req.Header.Get("Authorization"))
		status := r.status
		r.mu.Unlock()

		if status != 0 {
			w.WriteHeader(status)

			return
		}

		var out []byte

		switch req.URL.Path {
		case "/repos/o/pipelinegen-runner-concourse/releases/tags/v1":
			out = fmt.Appendf(nil, `{"assets": [{"url": "%[1]s/repos/o/pipelinegen-runner-concourse/releases/assets/3", "id": 3, "name": "concourse-runner.sbom"},
  {"url": "%[1]s/repos/o/pipelinegen-runner-concourse/releases/assets/4", "id": 4, "name": "concourse-runner-linux-amd64"}]}`, r.srv.URL)
		case "/repos/o/pipelinegen-runner-concourse/releases/tags/v2":
			out = []byte(`{"assets": []}`)
		case "/repos/o/pipelinegen-runner-concourse/releases/assets/4":
			out = r.body
		default:
			w.WriteHeader(http.StatusNotFound)
		}

		if _, err := w.Write(out); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(r.srv.Close)

	return r
}

func (r *runnerReleases) asked() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := r.asks
	r.asks = nil

	return out
}

func runnerPins(srv *httptest.Server, version, sum string, more ...string) []string {
	return append([]string{
		"GITHUB_TOKEN=secret-token-value", "PIPELINEGEN_GITHUB_API=" + srv.URL, "PIPELINEGEN_RUNNER=concourse",
		"PIPELINEGEN_RUNNER_PROGRAM=concourse-runner", "PIPELINEGEN_RUNNER_REPOSITORY=o/pipelinegen-runner-concourse",
		"PIPELINEGEN_RUNNER_VERSION=" + version, "PIPELINEGEN_RUNNER_SHA256=" + sum, "PIPELINEGEN_RUNNER_COMMIT=",
	}, more...)
}

// bootstrap runs the bootstrap as a task does, with the step's args after
// it, and returns what it printed and its exit code.
func bootstrap(t *testing.T, work string, env []string, step ...string) (string, int) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "sh", append([]string{"-ec", bootstrapScript, "bootstrap"}, step...)...)
	cmd.Dir, cmd.Env = work, env

	out, err := cmd.CombinedOutput()

	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		return string(out), exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}

	return string(out), 0
}

func TestTheBootstrapRunsThePinnedRunnersStep(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl")
	}

	rel := newRunnerReleases(t, []byte(fakeRunner))
	sum := sha256Hex(fakeRunner)
	work := t.TempDir()
	env := cleanEnv(t, runnerPins(rel.srv, "v1", sum, "PIPELINEGEN_PLANNER_ENV=GO_TOKEN", "GO_TOKEN=go")...)

	if out, code := bootstrap(t, work, env, "continuous-plan", "--plan-minutes", "60"); code != 0 || strings.Contains(out, "secret-token-value") {
		t.Fatalf("exit %d\n%s", code, out)
	}

	if got := readTestFile(t, filepath.Join(work, "ran")); string(got) != "step continuous-plan --plan-minutes 60\n" {
		t.Errorf("the runner ran %q", got)
	}

	if got := readEnv(t, filepath.Join(work, "runner.env")); got["GO_TOKEN"] != "go" {
		t.Error("the step lost the planner's secret, which its prepare command holds")
	}

	for _, p := range []string{"cache/concourse-runner-release-v1/concourse-runner", "tools/plugins/concourse"} {
		if got, err := os.ReadFile(filepath.Join(work, p)); err != nil || string(got) != fakeRunner {
			t.Errorf("%s: %q %v", p, got, err)
		}
	}

	asks := rel.asked()
	if len(asks) != 2 {
		t.Fatalf("asked %v", asks)
	}

	for _, a := range asks {
		if !strings.HasSuffix(a, " Bearer secret-token-value") {
			t.Errorf("a request without the token's header: %s", strings.SplitN(a, " ", 2)[0])
		}
	}

	// A runner the build placed is checked and run, from no cache.
	if err := os.RemoveAll(filepath.Join(work, "cache")); err != nil {
		t.Fatal(err)
	}

	if out, code := bootstrap(t, work, env, "start"); code != 0 || len(rel.asked()) != 0 {
		t.Errorf("a placed runner: exit %d\n%s", code, out)
	}

	// A wrong file under the runner's name is fetched again.
	writeExec(t, filepath.Join(work, "tools", "plugins", "concourse"), "#!/bin/sh\necho wrong\n")

	if out, code := bootstrap(t, work, env, "start"); code != 0 || len(rel.asked()) != 2 {
		t.Errorf("a wrong runner: exit %d\n%s", code, out)
	}

	if got := readTestFile(t, filepath.Join(work, "tools", "plugins", "concourse")); string(got) != fakeRunner {
		t.Errorf("the runner placed %q", got)
	}

	// The step's exit is the task's, a suite's end that could not be
	// recorded too.
	writeTestFile(t, filepath.Join(work, "trigger", "x"), "")

	if _, code := bootstrap(t, work, append(env, "RUNNER_EXIT=4"), "end"); code != 4 {
		t.Errorf("the end exited %d, not its step's 4", code)
	}
}

func TestTheBootstrapRefusesAWrongRunner(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl")
	}

	rel := newRunnerReleases(t, []byte("#!/bin/sh\necho tampered\n"))

	for _, c := range []struct {
		version, sum, says string
		code               int
	}{
		{"v1", sha256Hex(fakeRunner), "release v1 of o/pipelinegen-runner-concourse: concourse-runner-linux-amd64 doesn't match its sha256\n", 1},
		{"v2", sha256Hex(fakeRunner), "release v2 of o/pipelinegen-runner-concourse has no concourse-runner-linux-amd64\n", 1},
		{"", "", "the pipeline pins no runner: its config's plugins have none of kind runner\n", 1},
	} {
		work := t.TempDir()

		out, code := bootstrap(t, work, cleanEnv(t, runnerPins(rel.srv, c.version, c.sum)...), "start")
		if code != c.code || !strings.HasSuffix(out, c.says) {
			t.Errorf("%s: exit %d\n%s", c.version, code, out)
		}

		for _, p := range []string{"ran", "tools/plugins/concourse", "cache/concourse-runner-release-v1/concourse-runner"} {
			if exists(filepath.Join(work, p)) {
				t.Errorf("%s: %s is there", c.version, p)
			}
		}
	}
}

// The end runs on an abort too: what broke before it ends there with 0.
func TestTheBootstrapOfTheEnd(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl")
	}

	rel := newRunnerReleases(t, []byte(fakeRunner))
	env := cleanEnv(t, runnerPins(rel.srv, "v1", sha256Hex(fakeRunner))...)

	work := t.TempDir()
	if out, code := bootstrap(t, work, env, "end"); code != 0 || out != "no trigger: the build broke before it could report\n" || len(rel.asked()) != 0 {
		t.Errorf("no trigger: exit %d\n%s", code, out)
	}

	rel.status = http.StatusServiceUnavailable

	writeTestFile(t, filepath.Join(work, "trigger", "x"), "")

	out, code := bootstrap(t, work, env, "end")
	if code != 0 || !strings.HasSuffix(out, "release v1 of o/pipelinegen-runner-concourse has no concourse-runner-linux-amd64\n"+
		"the tools could not be fetched (exit 1): the build broke before it could report\n") {
		t.Errorf("unfetchable: exit %d\n%s", code, out)
	}

	// Any other step that can't fetch the runner fails.
	if _, code := bootstrap(t, work, env, "start"); code != 1 {
		t.Errorf("start without its runner: exit %d", code)
	}
}

// A runner pinned by its commit is built with git and go, which see none of
// the planner's params or secrets; the step sees them.
func TestTheBootstrapBuildsARunnersCommit(t *testing.T) {
	bin := t.TempDir()
	writeExec(t, filepath.Join(bin, "git"), "#!/bin/sh\necho \"git $*\" >> \"$TOOLS_LOG\"\n[ \"$1\" = init ] && mkdir -p \"$3\"\nexit 0\n")
	writeExec(t, filepath.Join(bin, "go"), "#!/bin/sh\necho \"go $* GOFLAGS=$GOFLAGS GO_TOKEN=${GO_TOKEN:-} GOPRIVATE=${GOPRIVATE:-}\" >> \"$TOOLS_LOG\"\n"+
		"cat > \"$3\" <<'EOF'\n"+fakeRunner+"EOF\nchmod +x \"$3\"\n")

	work := t.TempDir()
	log := filepath.Join(t.TempDir(), "tools.log")
	env := cleanEnv(t, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "TOOLS_LOG="+log, "GITHUB_TOKEN=x",
		"PIPELINEGEN_RUNNER=concourse", "PIPELINEGEN_RUNNER_PROGRAM=concourse-runner", "PIPELINEGEN_RUNNER_REPOSITORY=o/pipelinegen-runner-concourse",
		"PIPELINEGEN_RUNNER_VERSION=", "PIPELINEGEN_RUNNER_SHA256=", "PIPELINEGEN_RUNNER_COMMIT=c0ffee",
		"PIPELINEGEN_PLANNER_ENV=GO_TOKEN GOPRIVATE", "GO_TOKEN=go-token", "GOPRIVATE=github.com/o/*")

	if out, code := bootstrap(t, work, env, "plan"); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}

	want := "git init -q src\ngit -C src fetch -q --depth 1 https://github.com/o/pipelinegen-runner-concourse.git c0ffee\ngit -C src checkout -q FETCH_HEAD\n" +
		"go build -o ../cache/concourse-runner-commit-c0ffee/concourse-runner ./cmd/concourse-runner GOFLAGS=-trimpath GO_TOKEN= GOPRIVATE=\n"
	if got := string(readTestFile(t, log)); got != want {
		t.Errorf("built with\n%s\nwant\n%s", got, want)
	}

	if got := readEnv(t, filepath.Join(work, "runner.env")); got["GO_TOKEN"] != "go-token" || got["GOPRIVATE"] != "github.com/o/*" {
		t.Error("the step lost the planner's params and secrets")
	}

	// Built once per commit.
	if err := os.RemoveAll(filepath.Join(work, "tools")); err != nil {
		t.Fatal(err)
	}

	if out, code := bootstrap(t, work, env, "plan"); code != 0 || string(readTestFile(t, log)) != want {
		t.Errorf("built again: exit %d\n%s", code, out)
	}
}

func writeExec(t *testing.T, p, body string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(p, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}
