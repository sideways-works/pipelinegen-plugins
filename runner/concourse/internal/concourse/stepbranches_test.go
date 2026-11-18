package concourse

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// logTool is a stand-in for the tool that notes each call in calls.txt and
// then runs body.
func logTool(t *testing.T, work, body string) {
	t.Helper()

	writeFakeTool(t, work, "#!/bin/sh\necho \"$*\" >> calls.txt\n"+body)
}

func loggedCalls(t *testing.T, work string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(work, "calls.txt"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	return string(data)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))

	return hex.EncodeToString(sum[:])
}

func exists(p string) bool {
	_, err := os.Stat(p)

	return err == nil
}

func TestAStepEndsWithTheExitOfWhatFailed(t *testing.T) {
	for _, c := range []struct {
		name, step string
		args       []string
		tool       string
		env        []string
		code       int
		calls      string
	}{
		{
			name: "a prepare that fails plans nothing", step: "plan", env: []string{"PIPELINEGEN_PREPARE=exit 5"},
			code: 5,
		},
		{
			name: "the plan's exit", step: "plan", tool: "exit 3", code: 3,
			calls: "plan --run r --trigger trigger --root trigger --cache plan-cache\n",
		},
		{
			name: "a decision that fails", step: "continuous-plan", args: []string{"--plan-minutes", "60"},
			tool: "exit 6", env: []string{"PIPELINEGEN_PREPARE=touch ../prepared"}, code: 6,
			calls: "plan --decide --run r --trigger trigger --root trigger --cache plan-cache --gate handoff\n",
		},
		{
			name: "a continuous prepare that fails", step: "continuous-plan", args: []string{"--plan-minutes", "60"},
			env: []string{"PIPELINEGEN_PREPARE=exit 9"}, code: 9,
			calls: "plan --decide --run r --trigger trigger --root trigger --cache plan-cache --gate handoff\n",
		},
		{
			name: "begin's exit", step: "begin", tool: "exit 7", code: 7,
			calls: "plan --begin --run r --trigger trigger --root trigger --cache write-cache\n",
		},
		{
			name: "a proposed change's prepare that fails", step: "proposed-plan", env: []string{"PIPELINEGEN_PREPARE=exit 8"},
			code: 8,
		},
		{
			name: "the write's exit", step: "proposed-write", tool: "exit 2", code: 2,
			calls: "plan --run r --trigger trigger --root trigger --cache write-cache --from plan --gate handoff\n",
		},
		{
			name: "a list that fails", step: "sweep", tool: "exit 3", code: 3,
			calls: "unplanned --run r --root config --out sweep --cache write-cache\n",
		},
		{
			name: "a tree that fails", step: "start", tool: "exit 3", env: []string{"PIPELINEGEN_SUITE=e2e"}, code: 3,
			calls: "tree --run r --suite e2e --trigger trigger --out source --gate gate\n",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			work := t.TempDir()
			logTool(t, work, c.tool+"\n")
			writeTestFile(t, filepath.Join(work, "trigger", ".git", "resource", "version.json"), `{"commit":"abc","pr":"7"}`)

			for _, d := range []string{"base", "gate", "batches"} {
				if err := os.MkdirAll(filepath.Join(work, d), 0o750); err != nil {
					t.Fatal(err)
				}
			}

			out, code := taskStep(t, work, cleanEnv(t, append([]string{"PIPELINEGEN_RUN=r"}, c.env...)...), c.step, c.args...)
			if code != c.code {
				t.Errorf("exit %d, want %d\n%s", code, c.code, out)
			}

			if got := loggedCalls(t, work); got != c.calls {
				t.Errorf("calls:\n%s\nwant:\n%s", got, c.calls)
			}

			for _, p := range []string{"prepared", "gate/gate.json", "batches/batches.json"} {
				if exists(filepath.Join(work, p)) {
					t.Errorf("%s was written", p)
				}
			}
		})
	}
}

// The gate opens when the list wrote either directory, even an empty one.
func TestTheSweepsGate(t *testing.T) {
	for _, c := range []struct {
		dirs []string
		gate string
	}{
		{nil, "[]\n"},
		{[]string{"sweep/plan"}, `["1"]` + "\n"},
		{[]string{"sweep/handoff"}, `["1"]` + "\n"},
		{[]string{"sweep/plan/7"}, `["1"]` + "\n"},
	} {
		work := t.TempDir()
		logTool(t, work, "mkdir -p sweep "+strings.Join(c.dirs, " ")+"\n")

		if err := os.MkdirAll(filepath.Join(work, "gate"), 0o750); err != nil {
			t.Fatal(err)
		}

		if out := mustStep(t, work, cleanEnv(t, "PIPELINEGEN_RUN=r"), "sweep"); len(out) != 0 {
			t.Errorf("%v: printed %q", c.dirs, out)
		}

		if got := string(readTestFile(t, filepath.Join(work, "gate", "gate.json"))); got != c.gate {
			t.Errorf("%v: gate %q, want %q", c.dirs, got, c.gate)
		}
	}
}

func TestTheSweepSaysWhatItLeft(t *testing.T) {
	work := t.TempDir()

	for _, pr := range []string{"7", "8"} {
		writeTestFile(t, filepath.Join(work, "sweep", "plan", pr, ".git", "resource", "version.json"), `{"commit":"c`+pr+`","pr":"`+pr+`"}`)
	}

	writeTestFile(t, filepath.Join(work, "sweep", "plan", ".hidden", "x"), "")
	writeTestFile(t, filepath.Join(work, "sweep", "plan", "file"), "")
	writeTestFile(t, filepath.Join(work, "base", "README"), "")
	logTool(t, work, `case "$5" in sweep/head) grep -q '"pr":"8"' sweep/head/.git/resource/version.json && exit 1 ;; esac
mkdir -p plans/head
`)

	if err := os.MkdirAll(filepath.Join(work, "plans"), 0o750); err != nil {
		t.Fatal(err)
	}

	out := mustStep(t, work, cleanEnv(t, "PIPELINEGEN_RUN=r"), "sweep-plan")
	if got, want := string(out), "the sweep: pull request 8: not planned\n"; got != want {
		t.Errorf("sweep-plan says %q, want %q", got, want)
	}

	if !exists(filepath.Join(work, "plans", "7")) || exists(filepath.Join(work, "plans", "8")) {
		t.Error("the plans aren't the planned heads'")
	}

	// The prepare command failing leaves every head unplanned.
	work = t.TempDir()
	writeTestFile(t, filepath.Join(work, "sweep", "plan", "7", ".git", "resource", "version.json"), `{"commit":"c7","pr":"7"}`)
	writeTestFile(t, filepath.Join(work, "base", "README"), "")
	logTool(t, work, "")

	out = mustStep(t, work, cleanEnv(t, "PIPELINEGEN_RUN=r", "PIPELINEGEN_PREPARE=exit 1"), "sweep-plan")
	if got, want := string(out), "the sweep: pull request 7: not planned\n"; got != want {
		t.Errorf("sweep-plan says %q, want %q", got, want)
	}

	if got := loggedCalls(t, work); got != "" {
		t.Errorf("planned after its prepare failed: %s", got)
	}

	// Of the planned heads, 7's write fails and 8's gate stays shut.
	work = t.TempDir()

	for _, pr := range []string{"7", "8", "9"} {
		writeTestFile(t, filepath.Join(work, "sweep", "plan", pr, ".git", "resource", "version.json"), `{"commit":"c`+pr+`","pr":"`+pr+`"}`)
		writeTestFile(t, filepath.Join(work, "plans", pr, "plan.json"), "{}")
	}

	writeTestFile(t, filepath.Join(work, "sweep", "handoff", "11", ".git", "resource", "version.json"), `{"commit":"c11","pr":"11"}`)
	logTool(t, work, `head=$(cat sweep/head/.git/resource/version.json)
case "$head" in *'"pr":"7"'*) echo "plan: no" >&2; exit 1 ;; esac
mkdir -p "${13}"
case "$head" in *'"pr":"8"'*) echo '[]' > "${13}/gate.json" ;; *) echo '["1"]' > "${13}/gate.json" ;; esac
`)

	out = mustStep(t, work, cleanEnv(t, "PIPELINEGEN_RUN=r"), "sweep-write")
	if got, want := string(out), "plan: no\nthe sweep: pull request 7: its plan isn't written\nthe sweep hands off [\"9\",\"11\"]\n"; got != want {
		t.Errorf("sweep-write says %q, want %q", got, want)
	}

	if got := string(readTestFile(t, filepath.Join(work, "handoffs", "list.json"))); got != `["9","11"]`+"\n" {
		t.Errorf("list %q", got)
	}

	// With nothing to hand on, the list is empty.
	work = t.TempDir()
	logTool(t, work, "")

	if err := os.MkdirAll(filepath.Join(work, "handoffs"), 0o750); err != nil {
		t.Fatal(err)
	}

	if out := mustStep(t, work, cleanEnv(t, "PIPELINEGEN_RUN=r"), "sweep-write"); string(out) != "the sweep hands off []\n" {
		t.Errorf("an empty sweep says %q", out)
	}

	if got := string(readTestFile(t, filepath.Join(work, "handoffs", "list.json"))); got != "[]\n" {
		t.Errorf("empty list %q", got)
	}
}

func TestTheStaleChecksWords(t *testing.T) {
	long := "pipelinegen pipeline: config pipelinegen.yaml:\n\tline 1:\t" + strings.Repeat("x ", 200) + "\n"

	for _, c := range []struct {
		name, tool, hash string
		code             int
		out              string
	}{
		{
			name: "unreadable, cut short",
			tool: "cat >&2 <<'EOF'\nwarming up\n" + long + "EOF\nexit 2\n", code: 1,
			out: "the pipeline is stale: set it from the default branch with\n" +
				"  pipelinegen pipeline --out pipeline.yml && fly -t <target> set-pipeline -p fixture -c pipeline.yml\n" +
				"the pinned tool cannot read this tree's config: " + (("pipelinegen pipeline: config pipelinegen.yaml: line 1: " + strings.Repeat("x ", 200))[:300]) + "\n",
		},
		{
			name: "broken", tool: "echo out\necho 'it broke' >&2\nexit 2\n", code: 1,
			out: "out\nit broke\n",
		},
		{
			name: "current with the hash's line indented",
			tool: "mkdir -p newest && echo '[\"1\"]' > newest/gate.json\nprintf 'a: 1\\n\\t  PIPELINEGEN_PIPELINE_SHA256: abc\\nPIPELINEGEN_PIPELINE_SHA256x: 1\\n' > pipeline.yml\n",
			hash: "a: 1\n\t  PIPELINEGEN_PIPELINE_SHA256: \"\"\nPIPELINEGEN_PIPELINE_SHA256x: 1\n",
			out:  "the pipeline is current\n",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			work := t.TempDir()
			writeFakeTool(t, work, "#!/bin/sh\n"+c.tool)

			out, code := taskStep(t, work, cleanEnv(t, staleHash+"="+sha256Hex(c.hash), "PIPELINEGEN_PIPELINE=fixture"), "stale")
			if code != c.code || string(out) != c.out {
				t.Errorf("exit %d, want %d; said\n%q\nwant\n%q", code, c.code, out, c.out)
			}
		})
	}
}

func TestTheEndSaysWhyItEndsNothing(t *testing.T) {
	for _, c := range []struct {
		name    string
		trigger bool
		tool    string
		code    int
		out     string
	}{
		{name: "no trigger", tool: "exit 4", out: "no trigger: the build broke before it could report\n"},
		{name: "a failure its statuses show", trigger: true, tool: "exit 1", out: "the suite's end exited 1; its statuses say why\n"},
		{name: "unrecorded", trigger: true, tool: "exit 4", code: 4},
	} {
		t.Run(c.name, func(t *testing.T) {
			work := t.TempDir()
			writeFakeTool(t, work, "#!/bin/sh\n"+c.tool+"\n")

			if c.trigger {
				if err := os.MkdirAll(filepath.Join(work, "trigger"), 0o750); err != nil {
					t.Fatal(err)
				}
			}

			out, code := taskStep(t, work, cleanEnv(t), "end")
			if code != c.code || string(out) != c.out {
				t.Errorf("exit %d, want %d; said %q, want %q", code, c.code, out, c.out)
			}
		})
	}

	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, "trigger"), 0o750); err != nil {
		t.Fatal(err)
	}

	out, code := taskStep(t, work, cleanEnv(t, "PIPELINEGEN_TOOL_REPOSITORY=o/pipelinegen", "PIPELINEGEN_TOOL_COMMIT=",
		"PIPELINEGEN_TOOL_VERSION=v1", "PIPELINEGEN_TOOL_SHA256=", "PIPELINEGEN_GITHUB_API=http://127.0.0.1:1"), "end")
	// A release the API couldn't list has no files either.
	if !strings.HasPrefix(string(out), "no tools: the build broke before its start task had them; fetching them to end the suite\n") ||
		!strings.HasSuffix(string(out), "\nrelease v1 of o/pipelinegen has neither pipelinegen-linux-amd64 nor pipelinegen.mjs\n"+
			"the tools could not be fetched (exit 1): the build broke before it could report\n") || code != 0 {
		t.Errorf("unfetchable tools: exit %d\n%s", code, out)
	}
}

type posted struct {
	mu    sync.Mutex
	posts []string
}

func (p *posted) server(t *testing.T, status int) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}

		p.mu.Lock()
		p.posts = append(p.posts, r.Method+" "+r.URL.Path+" "+r.Header.Get("Accept")+" "+r.Header.Get("Content-Type")+" "+string(body))
		p.mu.Unlock()

		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)

	return srv
}

func TestTheErrorReportPostsWhatTheEndCannot(t *testing.T) {
	statuses := func(commit string) string {
		var b strings.Builder

		for _, ctx := range []string{"p/pr/e2e", "p/pr"} {
			b.WriteString("POST /repos/o/r/statuses/" + commit + " application/vnd.github+json application/x-www-form-urlencoded " +
				`{"state":"error","context":"` + ctx + `","description":"the build broke before it could report","target_url":"https://ci/x"}` + "\n")
		}

		return b.String()
	}

	for _, c := range []struct {
		name    string
		tool    string
		version string
		git     bool
		status  int
		code    int
		out     string
		posts   string
	}{
		{
			name: "a suite's end that broke", tool: "#!/bin/sh\nexit 2\n", version: `{"pr": "3",` + "\n" + ` "commit": "abc123"}`,
			out: "the suite's end exited 2: posting its status as errored\n", posts: statuses("abc123"),
		},
		{name: "no tools", version: `{"commit":"def456","x":{"commit":"0a1b"}}`, posts: statuses("0a1b")},
		{name: "no commit", version: `{"pr":"3"}`, out: "no trigger: nothing to post to\n"},
		{name: "no trigger", out: "no trigger: nothing to post to\n"},
		{name: "a checkout", git: true},
		{name: "refused", version: `{"commit":"abc"}`, status: http.StatusUnprocessableEntity, code: 22},
	} {
		t.Run(c.name, func(t *testing.T) {
			work := t.TempDir()
			if c.tool != "" {
				writeFakeTool(t, work, c.tool)
			}

			if c.version != "" {
				writeTestFile(t, filepath.Join(work, "trigger", ".git", "resource", "version.json"), c.version)
			}

			posts := c.posts

			if c.git {
				cmd := exec.CommandContext(t.Context(), "sh", "-ec", "git init -q trigger && git -C trigger -c user.name=t -c user.email=t@example.test commit -q --allow-empty -m x && git -C trigger rev-parse HEAD")
				cmd.Dir, cmd.Env = work, cleanEnv(t)

				head, err := cmd.Output()
				if err != nil {
					t.Fatal(err)
				}

				posts = statuses(strings.TrimSpace(string(head)))
			}

			status := c.status
			if status == 0 {
				status = http.StatusCreated
			}

			p := &posted{}
			srv := p.server(t, status)

			out, code := taskStep(t, work, cleanEnv(t, "GITHUB_TOKEN=x", "PIPELINEGEN_GITHUB_API="+srv.URL, "REPOSITORY=o/r",
				"SUITE_CONTEXT=p/pr/e2e", "RUN_CONTEXT=p/pr", "TARGET_URL=https://ci/x"), "report-error")
			if code != c.code {
				t.Errorf("exit %d, want %d\n%s", code, c.code, out)
			}

			if c.code == 0 && string(out) != c.out {
				t.Errorf("said %q, want %q", out, c.out)
			}

			p.mu.Lock()
			defer p.mu.Unlock()

			if got, want := strings.Join(p.posts, "\n"), strings.TrimSuffix(posts, "\n"); c.code == 0 && got != want {
				t.Errorf("posted:\n%s\nwant:\n%s", strings.Join(p.posts, "\n"), posts)
			}
		})
	}
}
