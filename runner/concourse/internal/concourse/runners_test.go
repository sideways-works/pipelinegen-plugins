package concourse

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

// twoRunners is the fixture as pipelinegen writes it for this runner when
// the config has two: the nightly is the other runner's.
func twoRunners(t *testing.T) (plugin.Spec, string) {
	t.Helper()

	spec, root := fixture(t)
	spec.Runner = "concourse"
	spec.Runs = slices.DeleteFunc(spec.Runs, func(r plugin.SpecRun) bool { return r.Name == "nightly" })
	spec.OtherRuns = []string{"nightly"}
	spec.Tools = append(spec.Tools,
		plugin.SpecTool{Name: "elsewhere", Kind: plugin.KindRunner, Repository: "o/pipelinegen-runner-elsewhere", Version: "v9", SHA256: "eee"},
		plugin.SpecTool{Name: "concourse", Kind: plugin.KindRunner, Repository: "https://github.com/o/plugins", Path: "runner/concourse", Version: "v1", SHA256: "abc"},
		plugin.SpecTool{Name: "go-driver", Kind: plugin.KindDriver, Repository: "git@github.com:o/plugins.git", Path: "driver/go", Commit: "c0ffee"},
	)

	return spec, root
}

func pipelineSteps(t *testing.T, p M) map[string][]string {
	t.Helper()

	out := map[string][]string{}

	walkTasks(jobNamed(t, p, "pipeline"), func(task M) {
		out[fmt.Sprint(task["task"])] = stepOf(asMap(asMap(task["config"])["run"]))
	})

	return out
}

func TestWithSeveralRunnersThePipelineIsWrittenForThisOne(t *testing.T) {
	spec, root := twoRunners(t)

	p := generated(t, spec, root, nil)

	if got := pipelineSteps(t, p)["stale"]; !slices.Equal(got, []string{"stale", "--for", "concourse"}) {
		t.Errorf("the stale check runs %v", got)
	}

	spec.CI["self_set"] = true
	if got := pipelineSteps(t, generated(t, spec, root, nil))["pipeline"]; !slices.Equal(got, []string{"pipeline", "--for", "concourse"}) {
		t.Errorf("the pipeline task runs %v", got)
	}

	for _, j := range asSlice(p["jobs"]) {
		if name := fmt.Sprint(asMap(j)["name"]); strings.Contains(name, "nightly") {
			t.Errorf("job %s of the other runner's run", name)
		}
	}

	seen := 0

	walkTasks(p, func(task M) {
		params := asMap(asMap(task["config"])["params"])
		if _, ok := params["PIPELINEGEN_PLUGINS"]; !ok {
			return
		}

		seen++

		for k, want := range map[string]string{
			"PIPELINEGEN_PLUGINS": "concourse https://github.com/o/plugins v1 abc - runner/concourse\n" +
				"go-driver git@github.com:o/plugins.git - - c0ffee driver/go\n",
			"PIPELINEGEN_PRIVATE":           "github.com/o/plugins",
			"PIPELINEGEN_RUNNER":            "concourse",
			"PIPELINEGEN_RUNNER_PROGRAM":    "concourse-runner",
			"PIPELINEGEN_RUNNER_REPOSITORY": "o/plugins",
			"PIPELINEGEN_RUNNER_PATH":       "runner/concourse",
			"PIPELINEGEN_RUNNER_VERSION":    "v1",
		} {
			if got := fmt.Sprint(params[k]); got != want {
				t.Errorf("%v: %s %q, want %q", task["task"], k, got, want)
			}
		}
	})

	if seen < 10 {
		t.Errorf("only %d tasks fetch the tools", seen)
	}
}

func TestWithOneRunnerThePipelineNamesNone(t *testing.T) {
	spec, root := fixture(t)
	spec.CI["self_set"] = true

	steps := pipelineSteps(t, generated(t, spec, root, nil))
	if got := steps["pipeline"]; !slices.Equal(got, []string{"pipeline"}) {
		t.Errorf("the pipeline task runs %v", got)
	}

	spec.CI["self_set"] = false
	if got := pipelineSteps(t, generated(t, spec, root, nil))["stale"]; !slices.Equal(got, []string{"stale"}) {
		t.Errorf("the stale check runs %v", got)
	}
}

func TestAnotherRunnersRunIsItsToPlace(t *testing.T) {
	spec, root := twoRunners(t)

	theirs := []any{
		on(forPlanning, []any{"x"}, "run", "nightly"),
		on(forSuite, []any{"nightly"}, "run", "nightly"),
		on(forSuite, []any{"nightly"}, "run", "nightly", "suite", "e2e"),
		on(forBookkeeping, []any{"tiny"}, "run", "nightly"),
	}
	ours := []any{on(forSuite, []any{"browsers"}, "suite", "e2e")}

	got, err := Text(generated(t, spec, root, placementOf([]any{"small"}, append(slices.Clone(theirs), ours...)...)))
	if err != nil {
		t.Fatal(err)
	}

	want, err := Text(generated(t, spec, root, placementOf([]any{"small"}, ours...)))
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != string(want) {
		t.Error("rules for another runner's run changed this pipeline")
	}

	if got := Notices(spec, placementOf(nil, theirs...)); len(got) == 0 || slices.ContainsFunc(got, func(n string) bool { return strings.Contains(n, "nightly") }) {
		t.Errorf("notices %q, want this runner's suite jobs alone", got)
	}

	for want, rule := range map[string]any{
		"no run weekly":                     on(forSuite, []any{"x"}, "run", "weekly"),
		"an empty tag":                      on(forSuite, []any{""}, "run", "nightly"),
		"tags is missing":                   map[string]any{"for": "suite", "run": "nightly"},
		"a rule for checks names no run":    on(forChecks, []any{"x"}, "run", "nightly"),
		"for is one of pipeline, planning,": on("tests", []any{"x"}, "run", "nightly"),
	} {
		if _, err := GenerateWith(spec, root, placementOf(nil, rule)); !errors.Is(err, ErrPlacement) || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: got %v, want %q", rule, err, want)
		}
	}
}

// A runner whose every run another runner owns writes the pipeline job
// alone, and the placement it had keeps.
func TestARunnerWithoutRuns(t *testing.T) {
	spec, root := twoRunners(t)
	spec.OtherRuns = []string{"main", "nightly", "pr"}
	spec.Runs = nil

	config := placementOf([]any{"small"},
		on(forPipeline, []any{"planner"}),
		on(forPlanning, []any{"planner"}),
		on(forSuite, []any{"browsers"}, "suite", "e2e"),
		on(forBookkeeping, []any{"tiny"}),
	)

	p := generated(t, spec, root, config)

	var jobs []string
	for _, j := range asSlice(p["jobs"]) {
		jobs = append(jobs, fmt.Sprint(asMap(j)["name"]))
	}

	if !slices.Equal(jobs, []string{"pipeline"}) {
		t.Errorf("jobs %v, want the pipeline job alone", jobs)
	}

	if got := placedSteps(p)["pipeline task stale"]; !slices.Equal(got, []string{"planner"}) {
		t.Errorf("the stale check placed on %v", got)
	}

	if got := Notices(spec, config); len(got) != 0 {
		t.Errorf("notices %q", got)
	}

	flyValidates(t, p)

	// a rule another one covers is still a mistake
	_, err := GenerateWith(spec, root, placementOf(nil, on(forPlanning, []any{"a"}), on(forPlanning, []any{"b"})))
	if !errors.Is(err, ErrPlacement) || !strings.Contains(err.Error(), "rule 2 {for: planning} places nothing: rule 1 {for: planning} comes first") {
		t.Errorf("a covered rule: %v", err)
	}
}

func TestThePipelineAndStaleStepsPassTheRunnersName(t *testing.T) {
	work := t.TempDir()
	logTool(t, work, "")

	mustStep(t, work, cleanEnv(t), "pipeline", "--for", "concourse")
	mustStep(t, work, cleanEnv(t, staleHash+"=x", "PIPELINEGEN_PIPELINE=fixture"), "stale", "--for", "concourse")
	mustStep(t, work, cleanEnv(t), "pipeline")

	want := "pipeline --root repo --gate pipeline --out pipeline/pipeline.yml --for concourse\n" +
		"pipeline --root repo --plugins tools/plugins --gate newest --out pipeline.yml --for concourse\n" +
		"pipeline --root repo --gate pipeline --out pipeline/pipeline.yml\n"
	if got := loggedCalls(t, work); got != want {
		t.Errorf("calls\n%s\nwant\n%s", got, want)
	}

	if out, code := taskStep(t, work, cleanEnv(t), "pipeline", "--for"); code != 2 || !strings.Contains(string(out), "usage: concourse-runner step pipeline [--for <runner>]") {
		t.Errorf("--for without a name: exit %d %s", code, out)
	}

	// the stale check's advice writes the same runner's pipeline
	writeFakeTool(t, work, "#!/bin/sh\nmkdir -p newest && echo '[\"1\"]' > newest/gate.json && echo changed > pipeline.yml\n")

	out, code := taskStep(t, work, cleanEnv(t, staleHash+"=x", "PIPELINEGEN_PIPELINE=fixture"), "stale", "--for", "concourse")
	if code != 1 || !strings.Contains(string(out), "  pipelinegen pipeline --for concourse --out pipeline.yml && fly") {
		t.Errorf("stale: exit %d\n%s", code, out)
	}
}
