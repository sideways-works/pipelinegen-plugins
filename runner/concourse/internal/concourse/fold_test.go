package concourse

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

// The error hooks run only when the build broke and are left out.
func jobTasks(job M) []string {
	var out []string

	walkTasksAny(job["plan"], &out)
	walkTasksAny(job["ensure"], &out)

	return out
}

func walkTasksAny(v any, out *[]string) {
	switch x := v.(type) {
	case M:
		if name, ok := x["task"].(string); ok {
			*out = append(*out, name)
		}

		for _, k := range []string{"in_parallel", "do", "try", "ensure", "across"} {
			walkTasksAny(x[k], out)
		}
	case []any:
		for _, e := range x {
			walkTasksAny(e, out)
		}
	}
}

func jobByName(t *testing.T, p M, name string) M {
	t.Helper()

	for _, j := range asSlice(p["jobs"]) {
		if job := asMap(j); job["name"] == name {
			return job
		}
	}

	t.Fatalf("no job %s", name)

	return nil
}

func TestJobsHoldFewTasks(t *testing.T) {
	for _, locked := range []bool{false, true} {
		spec, root := fixture(t)
		if locked {
			spec.Suites[suiteIndex(t, spec, "backend")].Lock = &plugin.SpecLock{Name: "db", Minutes: 20}
		}

		p, err := Generate(spec, root)
		if err != nil {
			t.Fatal(err)
		}

		if got := jobTasks(jobByName(t, p, "plan-main")); !slices.Equal(got, []string{"plan"}) {
			t.Errorf("lock %v: plan-main runs %v", locked, got)
		}

		if got := jobTasks(jobByName(t, p, "main-backend")); !slices.Equal(got, []string{"start", "batch", "batch-end", "end"}) {
			t.Errorf("lock %v: main-backend runs %v", locked, got)
		}

		for _, name := range []string{"pipeline"} {
			if got := jobTasks(jobByName(t, p, name)); len(got) != 1 {
				t.Errorf("lock %v: %s runs %v", locked, name, got)
			}
		}

		walkTasks(p, func(task M) {
			// The end task fetches them only when the build broke before its
			// start task had them, and nothing runs after it.
			if task["task"] == "end" {
				return
			}

			cfg := asMap(task["config"])
			if args := asSlice(asMap(cfg["run"])["args"]); len(args) == 2 && strings.Contains(fmt.Sprint(args[1]), bootstrapScript) &&
				!slices.Contains(mounts(cfg, "outputs"), "tools") {
				t.Errorf("%v fetches the tools without them as its output", task["task"])
			}
		})
	}
}

func TestTheEndRunsWhateverTheBuildDid(t *testing.T) {
	spec, root := fixture(t)

	p, err := Generate(spec, root)
	if err != nil {
		t.Fatal(err)
	}

	job := jobByName(t, p, "main-e2e")

	if end := asMap(job["ensure"]); end["task"] != "end" {
		t.Fatalf("the job's ensure is %v", job["ensure"])
	}

	found := false

	walkFileTasks(job, func(task M) {
		if task["task"] == "batch" {
			found = asMap(task["ensure"])["task"] == "batch-end"
		}
	})

	if !found {
		t.Error("the batch's end isn't in the batch's ensure")
	}
}

func TestOnlyTheLastCommandMayStartBazel(t *testing.T) {
	spec, root := fixture(t)

	p, err := Generate(spec, root)
	if err != nil {
		t.Fatal(err)
	}

	plans := 0

	walkTasks(p, func(task M) {
		calls := pipelinegenCalls(t, asMap(task["config"]))
		for i, c := range calls {
			if c[0] != "plan" {
				continue
			}

			plans++

			if i != len(calls)-1 {
				t.Errorf("%v: %v follows the plan", task["task"], calls[i+1:])
			}
		}
	})

	if plans == 0 {
		t.Fatal("no plan seen")
	}

	if !strings.Contains(planScript, "\nexec tools/pipelinegen plan ") {
		t.Error("the plan isn't the script's last command")
	}
}

func TestBookkeepingIsAChoice(t *testing.T) {
	for _, c := range []struct {
		name string
		tags []any
		want string
	}{
		{"no rule", nil, "[browsers]"},
		{"tagged", []any{"small"}, "[small]"},
		{"untagged", []any{}, "<nil>"},
	} {
		spec, root := fixture(t)

		var first []any
		if c.tags != nil {
			first = append(first, map[string]any{"for": "bookkeeping", "tags": c.tags})
		}

		own := c.name
		p := placed(t, spec, root, first...)

		job := jobByName(t, p, "main-e2e")
		seen := map[string]bool{}

		var walk func(v any)

		walk = func(v any) {
			switch x := v.(type) {
			case M:
				if name, ok := x["task"].(string); ok {
					tags := fmt.Sprint(x["tags"])

					want := "[browsers]"
					if name == "batch-end" || name == "end" {
						want = c.want
					}

					if tags != want {
						t.Errorf("%s: %s runs on %s, want %s", own, name, tags, want)
					}

					seen[name] = true
				}

				for _, e := range x {
					walk(e)
				}
			case []any:
				for _, e := range x {
					walk(e)
				}
			}
		}

		walk(job["plan"])
		walk(job["ensure"])

		for _, name := range []string{"start", "prepare", "batch", "batch-end", "end"} {
			if !seen[name] {
				t.Errorf("%s: no %s seen", own, name)
			}
		}
	}
}

func fakeTool(t *testing.T, work string, open bool, failing ...string) {
	t.Helper()

	gate := "[]"
	if open {
		gate = `["1"]`
	}

	var script strings.Builder

	script.WriteString("#!/bin/sh\necho \"$*\" >> calls.txt\ncase \"$1 $2\" in\n")
	script.WriteString("  tree*) mkdir -p gate && echo '" + gate + "' > gate/gate.json ;;\n")

	// A failing call exits 3, or the code it names after a "=".
	for _, f := range failing {
		call, code, ok := strings.Cut(f, "=")
		if !ok {
			code = "3"
		}

		script.WriteString("  \"" + call + "\") exit " + code + " ;;\n")
	}

	script.WriteString("esac\n")

	for _, d := range []string{"tools", "batches", "source", "trigger"} {
		if err := os.MkdirAll(filepath.Join(work, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.WriteFile(filepath.Join(work, "tools", "pipelinegen"), []byte(script.String()), 0o700); err != nil {
		t.Fatal(err)
	}
}

func calls(t *testing.T, work string) []string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(work, "calls.txt"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	var out []string

	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if line != "" {
			out = append(out, strings.Fields(line)[0]+" "+strings.Fields(line)[1])
		}
	}

	return out
}

func TestTheStartTask(t *testing.T) {
	for _, c := range []struct {
		open bool
		want []string
	}{
		{false, []string{"tree --run"}},
		{true, []string{"tree --run", "batches --run"}},
	} {
		work := t.TempDir()
		fakeTool(t, work, c.open)

		cmd := exec.CommandContext(t.Context(), "sh", "-ec", startScript)
		cmd.Dir, cmd.Env = work, cleanEnv(t, "PIPELINEGEN_RUN=main", "PIPELINEGEN_SUITE=e2e")

		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%+v: %v\n%s", c, err, out)
		}

		if got := calls(t, work); !slices.Equal(got, c.want) {
			t.Errorf("%+v: calls %v", c, got)
		}

		data, err := os.ReadFile(filepath.Join(work, "batches", "batches.json"))
		if !c.open && (err != nil || strings.TrimSpace(string(data)) != "[]") {
			t.Errorf("a shut gate left batches %q, %v", data, err)
		}
	}
}

// Only a suite's end that could not be recorded (exit 4) fails it.
func TestTheEndTask(t *testing.T) {
	for _, c := range []struct {
		tree, noTools bool
		failing       []string
		code          int
		want          []string
	}{
		{tree: false, want: []string{"report suite"}},
		{tree: true, want: []string{"report suite"}},
		{tree: true, failing: []string{"report suite=1"}, want: []string{"report suite"}},
		{tree: true, failing: []string{"report suite=4"}, code: 4, want: []string{"report suite"}},
		{noTools: true, want: nil},
	} {
		work := t.TempDir()

		fakeTool(t, work, true, c.failing...)

		if c.noTools {
			if err := os.RemoveAll(filepath.Join(work, "tools")); err != nil {
				t.Fatal(err)
			}
		}

		if c.tree {
			if err := os.WriteFile(filepath.Join(work, "source", "pipelinegen.yaml"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}

		cmd := exec.CommandContext(t.Context(), "sh", "-ec", endTaskScript())
		cmd.Dir, cmd.Env = work, cleanEnv(t)

		out, err := cmd.CombinedOutput()
		if code := cmd.ProcessState.ExitCode(); code != c.code {
			t.Errorf("%+v: exit %d (%v)\n%s", c, code, err, out)
		}

		if got := calls(t, work); !slices.Equal(got, c.want) {
			t.Errorf("%+v: calls %v", c, got)
		}
	}
}

func TestTheBootstrapNeverSeesThePlannersEnv(t *testing.T) {
	saved := bootstrapScript
	bootstrapScript = "env > boot.env\n"

	t.Cleanup(func() { bootstrapScript = saved })

	work := t.TempDir()

	cmd := exec.CommandContext(t.Context(), "sh", "-ec", withBootstrap("env > task.env\n"))
	cmd.Dir = work
	cmd.Env = cleanEnv(t, "GITHUB_TOKEN=pipeline-token", "GO_TOKEN=go-token", "GOPRIVATE=x",
		"PIPELINEGEN_PLANNER_ENV=GOPRIVATE GO_TOKEN GITHUB_TOKEN")

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	boot, task := readEnv(t, filepath.Join(work, "boot.env")), readEnv(t, filepath.Join(work, "task.env"))

	for _, k := range []string{"GO_TOKEN", "GOPRIVATE"} {
		if _, ok := boot[k]; ok {
			t.Errorf("the bootstrap sees %s", k)
		}

		if _, ok := task[k]; !ok {
			t.Errorf("the task lost %s", k)
		}
	}

	if boot["GITHUB_TOKEN"] != "pipeline-token" {
		t.Error("the bootstrap lost the pipeline's token")
	}
}

func TestTheEndTakesNoTreeEvenForAnOldLock(t *testing.T) {
	for _, locked := range []bool{false, true} {
		spec, root := fixture(t)
		if locked {
			spec.Suites[suiteIndex(t, spec, "backend")].Lock = &plugin.SpecLock{Name: "db", Minutes: 20}
		}

		p, err := Generate(spec, root)
		if err != nil {
			t.Fatal(err)
		}

		end := asMap(jobByName(t, p, "main-backend")["ensure"])
		got := fmt.Sprint(mounts(asMap(end["config"]), "inputs"))

		if want := "[tools trigger batches]"; got != want {
			t.Errorf("lock %v: the end task takes %s, want %s", locked, got, want)
		}
	}
}

func TestTheErrorReportEndsTheSuite(t *testing.T) {
	for _, c := range []struct {
		failing []string
		code    int
	}{
		{},
		{failing: []string{"report suite=1"}},
		{failing: []string{"report suite=4"}, code: 4},
	} {
		work := t.TempDir()
		fakeTool(t, work, true, c.failing...)

		cmd := exec.CommandContext(t.Context(), "sh", "-ec", fallbackScript)
		cmd.Dir, cmd.Env = work, cleanEnv(t, "PIPELINEGEN_GITHUB_API=http://127.0.0.1:1")

		out, err := cmd.CombinedOutput()
		if code := cmd.ProcessState.ExitCode(); code != c.code {
			t.Errorf("%+v: exit %d (%v)\n%s", c, code, err, out)
		}

		if got := calls(t, work); !slices.Equal(got, []string{"report suite"}) {
			t.Errorf("%+v: calls %v", c, got)
		}
	}
}
