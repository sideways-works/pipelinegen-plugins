package concourse

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func secretNames(spec plugin.Spec) []string {
	out := []string{"GITHUB_TOKEN"}

	for _, r := range spec.Runs {
		for _, rs := range r.Suites {
			for _, m := range []map[string]string{rs.Secrets.Prepare, rs.Secrets.Batch, rs.Secrets.Whole} {
				out = append(out, slices.Collect(maps.Keys(m))...)
			}
		}
	}

	if pl, ok := spec.CI["planner"].(map[string]any); ok {
		if sec, ok := pl["secrets"].(map[string]any); ok {
			out = append(out, slices.Collect(maps.Keys(sec))...)
		}
	}

	slices.Sort(out)

	return slices.Compact(out)
}

// expansions finds where the shell itself expands one of the variables. A
// single-quoted helper that git or awk runs later isn't the script's
// expansion.
func expansions(script string, vars []string) []string {
	var out []string

	single, double := false, false

	for line := range strings.SplitSeq(script, "\n") {
		for i := 0; i < len(line); i++ {
			c := line[i]

			switch {
			case single:
				single = c != '\''
			case c == '\\':
				i++
			case c == '\'' && !double:
				single = true
			case c == '"':
				double = !double
			case c == '#' && !double && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t'):
				i = len(line)
			case c == '$':
				rest := strings.TrimPrefix(line[i+1:], "{")
				for _, v := range vars {
					if strings.HasPrefix(rest, v) && (len(rest) == len(v) || !isNameChar(rest[len(v)])) {
						out = append(out, strings.TrimSpace(line))
					}
				}
			}
		}
	}

	return out
}

func isNameChar(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func scripts(p M) []string {
	var out []string

	walkTasks(p, func(task M) {
		run := asMap(asMap(task["config"])["run"])
		if args := asSlice(run["args"]); run["path"] == "sh" && len(args) == 2 {
			out = append(out, fmt.Sprint(args[1]))
		}
	})

	return out
}

// A trace or a child printing its environment would show an expanded
// token; git and curl read it from the environment themselves.
func TestNoScriptExpandsASecret(t *testing.T) {
	spec, root := fixture(t)
	vars := secretNames(spec)

	if !slices.Contains(vars, "GO_TOKEN") || !slices.Contains(vars, "NPM_TOKEN") {
		t.Fatalf("the fixture declares no secrets: %v", vars)
	}

	p, err := Generate(spec, root)
	if err != nil {
		t.Fatal(err)
	}

	all := append(scripts(p), bootstrapScript, planLib, planScript, proposedPlan, proposedWrite, sweepScript, sweepPlan, sweepWrite, staleScript, fallbackScript)
	if len(scripts(p)) < 4 {
		t.Fatalf("only %d scripts in the pipeline", len(scripts(p)))
	}

	for _, s := range all {
		for _, line := range expansions(s, vars) {
			t.Errorf("a script expands a secret: %s", line)
		}
	}

	for _, bad := range []string{
		`auth="Authorization: Bearer $GITHUB_TOKEN"`,
		`printf '%s' "${GITHUB_TOKEN}" | base64`,
		`export X="$NPM_TOKEN"`,
		"curl -H \"Authorization: Bearer ${GO_TOKEN:-}\" x",
	} {
		if len(expansions(bad, vars)) == 0 {
			t.Errorf("not caught: %s", bad)
		}
	}

	for _, fine := range []string{
		`export V='!f() { echo "password=${GITHUB_TOKEN}"; }; f'`,
		`# it isn't "$GITHUB_TOKEN" here`,
		`unset GITHUB_TOKEN`,
		`echo $GITHUB_TOKENS`,
	} {
		if got := expansions(fine, vars); len(got) > 0 {
			t.Errorf("caught by mistake: %s", fine)
		}
	}
}

var varRef = regexp.MustCompile(`\(\(([^()]*)\)\)`)

func TestPipelineReferencesOnlyItsVariables(t *testing.T) {
	spec, _ := fixture(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "own.yml"), []byte("resources:\n- name: own\n  type: git\n  source: {private_key: ((deploy_key))}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	spec.CI["include"] = "own.yml"

	p, err := Generate(spec, dir)
	if err != nil {
		t.Fatal(err)
	}

	allowed := map[string]bool{"github_token": true, "deploy_key": true, "npm_token": true, "report_key": true, "go_token": true}
	seen := map[string]bool{}

	for _, m := range varRef.FindAllStringSubmatch(text(t, p), -1) {
		name := m[1]
		if strings.HasPrefix(name, ".:") {
			continue // a var a step loads or an across sets
		}

		if !allowed[name] {
			t.Errorf("the pipeline references ((%s))", name)
		}

		seen[name] = true
	}

	for name := range allowed {
		if !seen[name] {
			t.Errorf("((%s)) is declared and never referenced", name)
		}
	}
}

var ownVars = map[string][]string{
	"prepare": {"PIPELINEGEN_RECORD", "PIPELINEGEN_RUN", "PIPELINEGEN_SUITE"},
	"batch": {
		"PIPELINEGEN_BATCH", "PIPELINEGEN_JOB_URL", "PIPELINEGEN_RECORD",
		"PIPELINEGEN_RUN", "PIPELINEGEN_RUN_FILE", "PIPELINEGEN_SUITE",
	},
	"whole": {"PIPELINEGEN_JOB_URL", "PIPELINEGEN_RECORD", "PIPELINEGEN_RUN", "PIPELINEGEN_SUITE"},
}

func TestEachTaskGetsItsKindsSecrets(t *testing.T) {
	spec, root := fixture(t)

	p, err := Generate(spec, root)
	if err != nil {
		t.Fatal(err)
	}

	byJob := map[string]plugin.SpecRunSuite{}

	for _, r := range spec.Runs {
		for _, rs := range r.Suites {
			byJob[SuiteJob(r.Name, rs.Suite)] = rs
		}
	}

	declared := secretNames(spec)
	kinds := map[string]int{}

	for _, j := range asSlice(p["jobs"]) {
		job := asMap(j)
		name := fmt.Sprint(job["name"])
		rs := byJob[name]

		walkTasks(job, func(task M) {
			params := asMap(asMap(task["config"])["params"])
			checkOwnStep(t, name, task, params, declared, rs)
		})

		walkFileTasks(job, func(task M) {
			kind := map[bool]string{true: "whole"}[strings.HasPrefix(fmt.Sprint(task["task"]), "whole.")]
			if kind == "" {
				kind = fmt.Sprint(task["task"])
			}

			secrets := map[string]map[string]string{"prepare": rs.Secrets.Prepare, "batch": rs.Secrets.Batch, "whole": rs.Secrets.Whole}[kind]
			want := M{}

			for k, v := range rs.Params {
				want[k] = v
			}

			for k, v := range secrets {
				want[k] = "((" + v + "))"
			}

			got := asMap(task["params"])

			var own []string

			for k, v := range got {
				if strings.HasPrefix(k, "PIPELINEGEN_") {
					own = append(own, k)
				} else if want[k] != v {
					t.Errorf("%s %s: %s=%v, want %v", name, task["task"], k, v, want[k])
				}
			}

			for k := range want {
				if _, ok := got[k]; !ok {
					t.Errorf("%s %s: no %s", name, task["task"], k)
				}
			}

			slices.Sort(own)

			if !slices.Equal(own, ownVars[kind]) {
				t.Errorf("%s %s: pipelinegen's variables %v, want %v", name, task["task"], own, ownVars[kind])
			}

			kinds[kind]++
		})
	}

	for _, k := range []string{"prepare", "batch", "whole"} {
		if kinds[k] == 0 {
			t.Errorf("no %s task seen", k)
		}
	}
}

func checkOwnStep(t *testing.T, job string, task M, params M, declared []string, rs plugin.SpecRunSuite) {
	t.Helper()

	run := asMap(asMap(task["config"])["run"])
	ownCall := run["path"] == "tools/pipelinegen" || strings.Contains(fmt.Sprint(run["args"]), "tools/pipelinegen ") || task["task"] == "report-error"

	if ownCall && params["GITHUB_TOKEN"] != "((github_token))" {
		t.Errorf("%s %s: pipelinegen's step without the pipeline's token", job, task["task"])
	}

	for k := range rs.Params {
		if _, ok := params[k]; ok {
			t.Errorf("%s %s: pipelinegen's step has the param %s", job, task["task"], k)
		}
	}

	// The plan steps hand the planner's secrets to their prepare command.
	if task["task"] == "plan" {
		return
	}

	for _, k := range declared {
		if _, ok := params[k]; ok && k != "GITHUB_TOKEN" {
			t.Errorf("%s %s: pipelinegen's step has the secret %s", job, task["task"], k)
		}
	}
}

func walkFileTasks(v any, f func(M)) {
	switch x := v.(type) {
	case M:
		if file, ok := x["file"].(string); ok && x["task"] != nil && strings.HasPrefix(file, "source/") {
			f(x)
		}

		for _, k := range slices.Sorted(maps.Keys(x)) {
			walkFileTasks(x[k], f)
		}
	case []any:
		for _, e := range x {
			walkFileTasks(e, f)
		}
	case []M:
		for _, e := range x {
			walkFileTasks(e, f)
		}
	}
}

func TestGenerationRefusesUnsafeEnv(t *testing.T) {
	cases := map[string]func(*plugin.Spec){
		"a param holding a var": func(s *plugin.Spec) { s.Runs[0].Suites[0].Params = map[string]string{"TOKEN": "((t))"} },
		"a PIPELINEGEN_ param": func(s *plugin.Spec) {
			s.Runs[0].Suites[0].Params = map[string]string{"PIPELINEGEN_RUN": "x"}
		},
		"a secret a param names": func(s *plugin.Spec) {
			s.Runs[0].Suites[0].Params = map[string]string{"NPM_TOKEN": "x"}
			s.Runs[0].Suites[0].Secrets.Prepare = map[string]string{"NPM_TOKEN": "npm_token"}
		},
		"a PIPELINEGEN_ secret": func(s *plugin.Spec) {
			s.Runs[0].Suites[0].Secrets.Batch = map[string]string{"PIPELINEGEN_X": "x"}
		},
		"a var that is no name": func(s *plugin.Spec) {
			s.Runs[0].Suites[0].Secrets.Whole = map[string]string{"KEY": "a)) ((b"}
		},
		"a planner param holding a var": func(s *plugin.Spec) {
			asMap(s.CI["planner"])["params"] = map[string]any{"X": "((y))"}
		},
		"a git token that is no secret": func(s *plugin.Spec) {
			asMap(s.CI["planner"])["git_token"] = "GOPRIVATE"
		},
		"the planner's PATH": func(s *plugin.Spec) {
			asMap(s.CI["planner"])["params"] = map[string]any{"PATH": "/x"}
		},
		"the planner's GITHUB_TOKEN from another var": func(s *plugin.Spec) {
			asMap(s.CI["planner"])["secrets"] = map[string]any{"GITHUB_TOKEN": "other"}
		},
	}

	for name, change := range cases {
		spec, root := fixture(t)
		change(&spec)

		if _, err := Generate(spec, root); !errors.Is(err, ErrEnv) {
			t.Errorf("%s: %v", name, err)
		}
	}

	spec, root := fixture(t)
	asMap(spec.CI["planner"])["secrets"] = map[string]any{"GITHUB_TOKEN": "github_token", "GO_TOKEN": "go_token"}

	if _, err := Generate(spec, root); err != nil {
		t.Errorf("the planner may have the pipeline's token by its var: %v", err)
	}
}

// cleanEnv tells git to read no global or system config, so no credential
// helper of the machine answers.
func cleanEnv(t *testing.T, more ...string) []string {
	t.Helper()

	home := t.TempDir()

	empty := filepath.Join(home, "empty.gitconfig")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	return append([]string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home, "XDG_CONFIG_HOME=" + home,
		"GIT_CONFIG_GLOBAL=" + empty, "GIT_CONFIG_NOSYSTEM=1", "TMPDIR=" + home,
	}, more...)
}

func TestPlannerEnvStaysWithThePrepare(t *testing.T) {
	for _, declared := range []bool{false, true} {
		work := t.TempDir()

		for _, d := range []string{"tools", "trigger"} {
			if err := os.MkdirAll(filepath.Join(work, d), 0o750); err != nil {
				t.Fatal(err)
			}
		}

		fake := "#!/bin/sh\nif [ \"$1\" = git-auth ]; then echo \"export GIT_AUTH_FOR=$2\"; exit 0; fi\nenv > plan.env\n"
		if err := os.WriteFile(filepath.Join(work, "tools", "pipelinegen"), []byte(fake), 0o700); err != nil {
			t.Fatal(err)
		}

		names := "GOPRIVATE GO_TOKEN"
		if declared {
			names += " GITHUB_TOKEN"
		}

		cmd := exec.CommandContext(t.Context(), "sh", "-ec", planLib+planScript)
		cmd.Dir = work
		cmd.Env = cleanEnv(t, "PIPELINEGEN_RUN=pr", "GITHUB_TOKEN=pipeline-token", "GOPRIVATE=github.com/o/*", "GO_TOKEN=go-token",
			"PIPELINEGEN_PLANNER_ENV="+names, "PIPELINEGEN_GIT_TOKEN=GO_TOKEN", "PIPELINEGEN_PREPARE=env > ../prepare.env")

		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}

		prepare, plan := readEnv(t, filepath.Join(work, "prepare.env")), readEnv(t, filepath.Join(work, "plan.env"))

		for k, v := range map[string]string{"GOPRIVATE": "github.com/o/*", "GO_TOKEN": "go-token", "GIT_AUTH_FOR": "GO_TOKEN"} {
			if prepare[k] != v {
				t.Errorf("declared %v: the prepare command's %s is %q", declared, k, prepare[k])
			}

			if _, ok := plan[k]; ok {
				t.Errorf("declared %v: the plan sees %s", declared, k)
			}
		}

		if _, ok := prepare["GITHUB_TOKEN"]; ok != declared {
			t.Errorf("declared %v: the prepare command has the pipeline's token: %v", declared, ok)
		}

		if plan["GITHUB_TOKEN"] != "pipeline-token" {
			t.Errorf("declared %v: the plan lost the pipeline's token", declared)
		}
	}
}

func readEnv(t *testing.T, file string) map[string]string {
	t.Helper()

	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	out := map[string]string{}

	for line := range strings.SplitSeq(string(data), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			out[k] = v
		}
	}

	return out
}

// The answer is compared here and never printed.
func TestBootstrapGitAsksTheVariable(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}

	begin, end := strings.Index(bootstrapScript, "# git-auth:begin\n"), strings.Index(bootstrapScript, "# git-auth:end\n")
	if begin < 0 || end < begin {
		t.Fatal("the bootstrap's git lines aren't marked")
	}

	lines := bootstrapScript[begin:end]

	for host, want := range map[string]bool{"github.com": true, "example.com": false} {
		script := lines + "printf 'protocol=https\\nhost=" + host + "\\n\\n' | git credential fill\n"

		cmd := exec.CommandContext(t.Context(), "sh", "-ec", script)
		cmd.Env = cleanEnv(t, "GITHUB_TOKEN=fake-token-value")

		out, err := cmd.Output()

		got := err == nil && strings.Contains(string(out), "\npassword=fake-token-value\n") &&
			strings.Contains(string(out), "username=x-access-token\n")
		if got != want {
			t.Errorf("%s: the helper answered %v, want %v (output withheld)", host, got, want)
		}
	}
}

type headers struct {
	mu   sync.Mutex
	auth []string
}

func (h *headers) note(r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.auth = append(h.auth, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
}

func TestCurlTakesItsHeaderFromStdin(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl")
	}

	h := &headers{}
	binary := []byte("#!/bin/sh\n")
	sum := sha256.Sum256(binary)

	var base string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.note(r)

		var body []byte

		switch r.URL.Path {
		case "/repos/o/pipelinegen/releases/tags/v1":
			body = release(base, "pipelinegen")
		case "/repos/o/go-sbom/releases/tags/v1":
			body = release(base, "go-sbom")
		case "/repos/o/pipelinegen/releases/assets/7", "/repos/o/go-sbom/releases/assets/7":
			body = binary
		default:
			w.WriteHeader(http.StatusCreated)
		}

		if _, err := w.Write(body); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()

	base = srv.URL

	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, "trigger", ".git", "resource"), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(work, "trigger", ".git", "resource", "version.json"), []byte(`{"pr":"3","commit":"abc123"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(t.Context(), "sh", "-ec", fallbackScript)
	cmd.Dir = work
	cmd.Env = cleanEnv(t, "GITHUB_TOKEN=secret-value", "PIPELINEGEN_GITHUB_API="+srv.URL, "REPOSITORY=o/r",
		"SUITE_CONTEXT=p/pr/e2e", "RUN_CONTEXT=p/pr", "TARGET_URL=https://ci.example/jobs/x")

	out, err := cmd.CombinedOutput()
	if err != nil || strings.Contains(string(out), "secret-value") {
		t.Fatalf("report-error: %v\n%s", err, out)
	}

	work = t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, "cache"), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(work, "cache", "bazelisk-v1.27.0"), binary, 0o700); err != nil {
		t.Fatal(err)
	}

	hex := hex.EncodeToString(sum[:])

	cmd = exec.CommandContext(t.Context(), "sh", "-ec", bootstrapScript)
	cmd.Dir = work
	cmd.Env = cleanEnv(t, "GITHUB_TOKEN=secret-value", "PIPELINEGEN_GITHUB_API="+srv.URL,
		"PIPELINEGEN_TOOL_REPOSITORY=o/pipelinegen", "PIPELINEGEN_TOOL_VERSION=v1", "PIPELINEGEN_TOOL_SHA256="+hex,
		"PIPELINEGEN_TOOL_COMMIT=", "PIPELINEGEN_BAZELISK_VERSION=v1.27.0", "PIPELINEGEN_BAZELISK_SHA256="+hex,
		"PIPELINEGEN_PLUGINS=go o/go-sbom v1 "+hex+" -\n", "PIPELINEGEN_PRIVATE=")

	if out, err := cmd.CombinedOutput(); err != nil || strings.Contains(string(out), "secret-value") {
		t.Fatalf("bootstrap: %v\n%s", err, out)
	}

	for _, p := range []string{"tools/pipelinegen", "tools/plugins/go"} {
		if _, err := os.Stat(filepath.Join(work, p)); err != nil {
			t.Error(err)
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.auth) != 6 {
		t.Fatalf("requests %v", h.auth)
	}

	for _, a := range h.auth {
		if !strings.HasSuffix(a, " Bearer secret-value") {
			t.Errorf("a request without the token's header: %s", strings.SplitN(a, " Bearer", 2)[0])
		}
	}
}

func release(base, name string) []byte {
	return fmt.Appendf(nil, `{"assets": [{"url": "%s/repos/o/%s/releases/assets/7", "id": 7, "name": "%s-linux-amd64"}]}`, base, name, name)
}

func TestPrepareAndChecksKnowWhetherTheRunRecords(t *testing.T) {
	spec, root := fixture(t)

	p, err := Generate(spec, root)
	if err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}

	for _, j := range asSlice(p["jobs"]) {
		job := asMap(j)
		name := fmt.Sprint(job["name"])
		record := map[string]any{}

		walkFileTasks(job, func(task M) {
			kind := fmt.Sprint(task["task"])
			if strings.HasPrefix(kind, "whole.") {
				kind = "whole"
			}

			v, ok := asMap(task["params"])["PIPELINEGEN_RECORD"]
			if !ok {
				t.Errorf("%s %s: no PIPELINEGEN_RECORD", name, task["task"])
			}

			record[kind] = v
		})

		if len(record) == 0 {
			continue
		}

		for _, kind := range []string{"prepare", "whole"} {
			if v, ok := record[kind]; ok {
				seen[kind+" "+fmt.Sprint(v)] = true

				if v != record["batch"] {
					t.Errorf("%s: the %s's PIPELINEGEN_RECORD %v, the batch's %v", name, kind, v, record["batch"])
				}
			}
		}
	}

	for _, want := range []string{"prepare true", "whole true"} {
		if !seen[want] {
			t.Errorf("no %s seen: %v", want, seen)
		}
	}
}
