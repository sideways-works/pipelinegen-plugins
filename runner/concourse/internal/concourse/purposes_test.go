package concourse

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func TestSplitTokensReachEachStepByPurpose(t *testing.T) {
	spec, root := splitSpec(t)

	p, err := Generate(spec, root)
	if err != nil {
		t.Fatal(err)
	}

	ends := map[string][]string{}

	for _, j := range asSlice(p["jobs"]) {
		job := asMap(j)

		walkTasks(job, func(task M) {
			params := asMap(asMap(task["config"])["params"])
			if _, ok := params["GITHUB_TOKEN"]; ok {
				t.Errorf("%s %s: GITHUB_TOKEN with the tokens split", job["name"], task["task"])
			}

			if strings.Contains(asString(asMap(task["config"])["run"]), "git-auth:begin") && params["PIPELINEGEN_READ_TOKEN"] != "((reader))" {
				t.Errorf("%s %s: bootstraps without the read token", job["name"], task["task"])
			}

			// The error report may end the suite itself, so it holds what the end task
			// holds.
			if task["task"] == "report-error" || task["task"] == "end" {
				ends[fmt.Sprint(job["name"])] = append(ends[fmt.Sprint(job["name"])], strings.Join(refVars(params), " "))
			}
		})
	}

	for job, tokens := range ends {
		if len(tokens) < 2 || slices.ContainsFunc(tokens, func(x string) bool { return x != tokens[0] }) {
			t.Errorf("%s: the error report and the end task hold %v", job, tokens)
		}
	}

	for _, name := range []string{"trigger-pr", "planned-pr"} {
		if got := asMap(resourceNamed(t, p, name)["source"])["access_token"]; got != "((lister))" {
			t.Errorf("%s: access token %v", name, got)
		}
	}
}

func asString(v any) string {
	if m, ok := v.(M); ok {
		var b strings.Builder

		for _, a := range asSlice(m["args"]) {
			if s, ok := a.(string); ok {
				b.WriteString(s)
			}
		}

		return b.String()
	}

	return ""
}

func TestAProposedChangesTasksGetAtMostTheReadToken(t *testing.T) {
	declare := func(run, kind, v string) plugin.Spec {
		spec, _ := splitSpec(t)
		r := runIndex(t, spec, run)

		for i := range spec.Runs[r].Suites {
			s := &spec.Runs[r].Suites[i].Secrets

			switch kind {
			case "batch":
				s.Batch = map[string]string{"GH": v}
			case "prepare":
				s.Prepare = map[string]string{"GH": v}
			}
		}

		return spec
	}

	for _, c := range []struct {
		run, kind, v string
		refused      bool
	}{
		{"pr", "batch", "storer", true},
		{"pr", "prepare", "reporter", true},
		{"pr", "batch", "lister", true},
		{"pr", "batch", "reader", false},
		{"main", "batch", "storer", false},
	} {
		_, err := Generate(declare(c.run, c.kind, c.v), t.TempDir())
		if errors.Is(err, ErrEnv) != c.refused {
			t.Errorf("%s %s %s: %v", c.run, c.kind, c.v, err)
		}
	}

	spec, root := splitSpec(t)
	asMap(spec.CI["planner"])["secrets"] = map[string]any{"GH": "storer"}

	if _, err := Generate(spec, root); !errors.Is(err, ErrEnv) {
		t.Errorf("a planner secret that writes: %v", err)
	}
}

func TestTheScriptsTakeTheirPurposesToken(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}

	cmd := exec.CommandContext(t.Context(), "git", "credential", "fill")
	cmd.Env = append(cleanEnv(t, "GITHUB_TOKEN=one-token-value", "PIPELINEGEN_READ_TOKEN=read-token-value"), bootstrapGitEnv(t)...)
	cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\n\n")

	if out, err := cmd.Output(); err != nil || !strings.Contains(string(out), "\npassword=read-token-value\n") {
		t.Errorf("git answered without the read token: %v (output withheld)", err)
	}

	var auth []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))

		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	work := t.TempDir()
	writeTestFile(t, filepath.Join(work, "trigger", ".git", "resource", "version.json"), `{"pr":"3","commit":"abc123"}`)

	out, code := taskStep(t, work, cleanEnv(t, "GITHUB_TOKEN=one-token-value", "PIPELINEGEN_REPORT_TOKEN=report-token-value",
		"PIPELINEGEN_GITHUB_API="+srv.URL, "REPOSITORY=o/r", "SUITE_CONTEXT=p/pr/e2e", "RUN_CONTEXT=p/pr", "TARGET_URL=https://ci/x"), "report-error")
	if code != 0 || strings.Contains(string(out), "token-value") {
		t.Fatalf("report-error: exit %d\n%s", code, out)
	}

	if len(auth) != 2 || auth[0] != "Bearer report-token-value" || auth[1] != auth[0] {
		t.Errorf("the error hook's statuses carried %d headers, not the report token's", len(auth))
	}
}
