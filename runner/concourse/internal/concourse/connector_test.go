package concourse

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func findJob(p M, name string) M {
	jobs, _ := p["jobs"].([]any) //nolint:errcheck // none: no job

	for _, j := range jobs {
		if m, ok := j.(M); ok && m["name"] == name {
			return m
		}
	}

	return nil
}

func findResource(p M, name string) M {
	res, _ := p["resources"].([]any) //nolint:errcheck // none: no resource

	for _, r := range res {
		if m, ok := r.(M); ok && m["name"] == name {
			return m
		}
	}

	return nil
}

func TestAConnectorsRunIsSweptPlannedAndHandedOn(t *testing.T) {
	spec, root := fixture(t)

	for i, r := range spec.Runs {
		if r.Trigger.Kind == "pull_requests" {
			spec.Runs[i].Trigger.Connector, spec.Runs[i].Trigger.SweepEvery = "git", "1m"
		}
	}

	p, err := Generate(spec, root)
	if err != nil {
		t.Fatal(err)
	}

	out := text(t, p)
	if strings.Contains(out, "type: github-pr") || findJob(p, "plan-pr") != nil {
		t.Fatal("a connector's run has a pull request resource or a planning job")
	}

	planned := findResource(p, "planned-pr")
	if planned["type"] != "mock" || planned["check_every"] != "never" {
		t.Fatalf("hand-off %v", planned)
	}

	if src, _ := planned["source"].(M); src["no_initial_version"] != true || src["log"] != "pipelinegen hand-off: "+spec.Pipeline+" pr planned" { //nolint:errcheck // checked by its fields
		t.Errorf("hand-off source %v", planned["source"])
	}

	if timer, _ := findResource(p, "sweep-pr-timer")["source"].(M); timer["interval"] != "1m" { //nolint:errcheck // checked by its field
		t.Errorf("sweep timer %v", timer)
	}

	sweep := text(t, findJob(p, "sweep-pr"))
	if strings.Contains(sweep, "get: planned") || !strings.Contains(sweep, "file: handoffs/((.:handoff))/run.json") ||
		!strings.Contains(sweep, `cp "$1/run.json" "$2/run.json"`) {
		t.Errorf("the sweep job:\n%s", sweep)
	}

	for _, rs := range spec.Runs {
		if rs.Name != "pr" {
			continue
		}

		for _, s := range rs.Suites {
			job := findJob(p, SuiteJob("pr", s.Suite))
			if _, ok := job["on_error"]; ok {
				t.Errorf("%s posts a fallback status", s.Suite)
			}

			steps, _ := job["plan"].([]any)              //nolint:errcheck // checked by its length
			second, _ := steps[min(1, len(steps)-1)].(M) //nolint:errcheck // checked below

			if len(steps) < 2 || !strings.Contains(text(t, M{"s": steps[0]}), "get: handoff") || second["task"] != "trigger" {
				t.Errorf("%s begins %v", s.Suite, steps[:min(2, len(steps))])
			}
		}
	}
}

func TestAHandOffReachesTheTriggerAsItWasWritten(t *testing.T) {
	dir := t.TempDir()
	runJSON := `{"run":"pr","change":"git/branch-review%2Fcart","commit":"0123456789abcdef0123456789abcdef01234567","handed":"2026-11-14T10:00:00Z-ab"}` + "\n"

	if err := os.MkdirAll(filepath.Join(dir, "sweep", "head"), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "sweep", "head", "run.json"), []byte(runJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	sh := func(script string) {
		t.Helper()

		cmd := exec.CommandContext(t.Context(), "sh", "-ec", script)
		cmd.Dir = dir

		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}

	sh(planLib + connectorHandOff + `handoff sweep/head handoffs/x`)

	data, err := os.ReadFile(filepath.Join(dir, "handoffs", "x", "run.json"))
	if err != nil || string(data) != runJSON {
		t.Fatalf("handed on %q %v", data, err)
	}

	if err := os.MkdirAll(filepath.Join(dir, "handoff"), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "handoff", "version"), data, 0o600); err != nil { //nolint:gosec // the test's own directory
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(dir, "trigger"), 0o750); err != nil {
		t.Fatal(err)
	}

	cfg, _ := handOffTrigger(nil)["config"].(M) //nolint:errcheck // checked below
	run, _ := cfg["run"].(M)                    //nolint:errcheck // checked below
	args, _ := run["args"].([]any)              //nolint:errcheck // checked below

	script, _ := args[1].(string) //nolint:errcheck // an empty script copies nothing, and the check below fails

	sh(script)

	if got, err := os.ReadFile(filepath.Join(dir, "trigger", "run.json")); err != nil || string(got) != runJSON {
		t.Fatalf("the trigger's run.json %q %v", got, err)
	}
}
