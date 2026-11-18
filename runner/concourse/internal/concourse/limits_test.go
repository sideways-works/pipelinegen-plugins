package concourse

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

type rooted struct {
	spec plugin.Spec
	root string
}

func limitSpecs(t *testing.T) map[string]rooted {
	t.Helper()

	out := map[string]rooted{}
	for name, s := range steppedSpecs(t) {
		out[name] = rooted{s, t.TempDir()}
	}

	perBatch, _ := fixture(t)
	for i := range perBatch.Suites {
		perBatch.Suites[i].Lock = &plugin.SpecLock{Name: "stack", Slots: 2, Per: "batch"}
	}

	perBatch.Suites[suiteIndex(t, perBatch, "e2e")].WholeSuite = []plugin.SpecWholeSuite{{Name: "money", Task: "ci/money.yml"}}
	out["an older tool's lock per batch"] = rooted{perBatch, t.TempDir()}

	cont, _ := fixture(t)
	cont.Suites[suiteIndex(t, cont, "e2e")].Lock = &plugin.SpecLock{Name: "browsers"}
	full := cont.Runs[runIndex(t, cont, "nightly")]
	full.Name, full.Trigger, full.WaitMinutes = "full", plugin.SpecTrigger{Kind: triggerContinuous, Branch: "main"}, 300
	cont.Runs = append(cont.Runs, full)
	out["continuous"] = rooted{cont, t.TempDir()}

	images, root := withImages(t, true)
	out["images"] = rooted{images, root}

	shared, _ := fixture(t)
	sharedRoot := t.TempDir()

	if err := os.WriteFile(filepath.Join(sharedRoot, "root.yml"), []byte("resources:\n- name: twin\n  type: registry-image\n  source: {repository: golang, tag: \"1.27.1\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	shared.CI["include"] = "root.yml"
	shared.Suites[suiteIndex(t, shared, "e2e")].Image = "twin"
	out["the planner's image"] = rooted{shared, sharedRoot}

	return out
}

func TestEveryStepHasATimeLimit(t *testing.T) {
	for name, r := range limitSpecs(t) {
		p, err := Generate(r.spec, r.root)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		for _, j := range asSlice(p["jobs"]) {
			job := asMap(j)
			where := fmt.Sprintf("%s: job %v", name, job["name"])

			for _, problem := range unbounded(job["plan"], false) {
				t.Errorf("%s: %s", where, problem)
			}

			for _, h := range []string{"ensure", "on_success", "on_failure", "on_error", "on_abort"} {
				for _, problem := range unbounded(job[h], false) {
					t.Errorf("%s, its %s: %s", where, h, problem)
				}
			}
		}
	}
}

func unbounded(v any, bounded bool) []string {
	var out []string

	switch x := v.(type) {
	case []any:
		for _, e := range x {
			out = append(out, unbounded(e, bounded)...)
		}
	case M:
		inner := bounded || x["timeout"] != nil

		for _, kind := range []string{"task", "get", "put", "set_pipeline"} {
			if _, ok := x[kind]; ok && !inner {
				out = append(out, fmt.Sprintf("%s %v has no time limit", kind, x[kind]))
			}
		}

		for _, k := range []string{"do", "in_parallel", "steps", "try"} {
			out = append(out, unbounded(x[k], inner)...)
		}

		for _, h := range []string{"ensure", "on_success", "on_failure", "on_error", "on_abort"} {
			out = append(out, unbounded(x[h], false)...)
		}
	}

	return out
}

func TestEachStepHasItsLimit(t *testing.T) {
	spec, root := fixture(t)
	spec.Limits = plugin.SpecLimits{Plan: 45, Steps: 15}
	spec.Suites[suiteIndex(t, spec, "backend")].Lock = &plugin.SpecLock{Name: "db"}

	for i := range spec.Runs {
		for j := range spec.Runs[i].Suites {
			spec.Runs[i].Suites[j].Minutes = 70
		}
	}

	full := spec.Runs[runIndex(t, spec, "nightly")]
	full.Name, full.Trigger, full.WaitMinutes = "full", plugin.SpecTrigger{Kind: triggerContinuous, Branch: "main"}, 300
	spec.Runs = append(spec.Runs, full)

	p, err := Generate(spec, root)
	if err != nil {
		t.Fatal(err)
	}

	limits := map[string]any{}

	for _, job := range []string{"pr-e2e", "pr-backend", "plan-full", "plan-main", "plan-pr", "sweep-pr"} {
		walkMaps(jobByName(t, p, job), func(m M) {
			if name, ok := m["task"].(string); ok {
				limits[job+"/"+name] = m["timeout"]
			}
		})
	}

	e2e := jobByName(t, p, "pr-e2e")
	walkMaps(e2e, func(m M) {
		if _, ok := m["across"]; ok && m["timeout"] != nil {
			t.Errorf("a suite bounds its whole work: %v", m["timeout"])
		}
	})

	for task, want := range map[string]string{
		"pr-e2e/start": "15m", "pr-e2e/batch": "70m", "pr-e2e/batch-end": "15m", "pr-e2e/end": "15m",
		"pr-backend/start": "15m", "pr-backend/batch": "70m",
		"plan-full/plan": "345m", "plan-main/plan": "45m", "plan-pr/plan": "45m", "plan-pr/write": "45m",
		"sweep-pr/sweep": "45m", "sweep-pr/plan": "45m", "sweep-pr/write": "45m",
	} {
		if got := limits[task]; got != want {
			t.Errorf("%s: limit %v, want %s", task, got, want)
		}
	}

	var start, batchEnd M

	walkMaps(e2e, func(m M) {
		switch m["task"] {
		case "start":
			start = m
		case "batch-end":
			batchEnd = m
		}
	})

	if got := asMap(asMap(batchEnd["config"])["params"])[plugin.EnvLimit]; got != "limits.steps=15" {
		t.Errorf("batch-end is told %v", got)
	}

	if got := asMap(asMap(start["config"])["params"])[plugin.EnvLimit]; got != "limits.steps=15" {
		t.Errorf("start is told %v", got)
	}

	if script := fmt.Sprint(asMap(asMap(start["config"])["run"])["args"]); !strings.Contains(script, "export PIPELINEGEN_DEADLINE=$(( $(date +%s) + 900 ))") {
		t.Errorf("the start task's script stamps no deadline: %s", script)
	}
}

func walkMaps(v any, f func(M)) {
	switch x := v.(type) {
	case M:
		f(x)

		for _, e := range x {
			walkMaps(e, f)
		}
	case []any:
		for _, e := range x {
			walkMaps(e, f)
		}
	case []M:
		for _, e := range x {
			walkMaps(e, f)
		}
	}
}
