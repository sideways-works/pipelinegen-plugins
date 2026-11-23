package concourse

import (
	"fmt"
	"slices"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

// holdersOf names, as job/task, each task whose params hold the variable.
func holdersOf(t *testing.T, p M, variable string) []string {
	t.Helper()

	jobs, ok := p["jobs"].([]any)
	if !ok {
		t.Fatalf("no jobs: %T", p["jobs"])
	}

	var out []string

	for _, j := range jobs {
		job := asMap(j)
		walkMaps(job, func(m M) {
			if _, ok := m["task"]; !ok {
				return
			}

			if _, ok := asMap(asMap(m["config"])["params"])[variable]; ok {
				out = append(out, fmt.Sprintf("%s/%s", job["name"], m["task"]))
			}
		})
	}

	slices.Sort(out)

	return slices.Compact(out)
}

func TestOnlyTheStepsThatWriteAPlanHoldThePlansKey(t *testing.T) {
	spec, root := fixture(t)
	spec.Plans = &plugin.SpecPlans{AccessKeyID: "plans_key_id", SecretAccessKey: "plans_secret"}

	full := spec.Runs[runIndex(t, spec, "nightly")]
	full.Name, full.Trigger = "full", plugin.SpecTrigger{Kind: triggerContinuous, Branch: "main"}
	spec.Runs = append(spec.Runs, full)

	p, err := Generate(spec, root)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"plan-full/plan", "plan-main/plan", "plan-nightly/plan", "plan-pr/write", "sweep-pr/write"}

	for _, v := range []string{plugin.EnvPlansAccessKeyID, plugin.EnvPlansSecretAccessKey} {
		if got := holdersOf(t, p, v); !slices.Equal(got, want) {
			t.Errorf("%s is held by %v, want %v", v, got, want)
		}
	}

	var write M

	walkMaps(jobByName(t, p, "plan-pr"), func(m M) {
		if m["task"] == "write" {
			write = m
		}
	})

	if got := asMap(asMap(write["config"])["params"])[plugin.EnvPlansSecretAccessKey]; got != "((plans_secret))" {
		t.Errorf("the write task's secret is %v", got)
	}
}

func TestWithoutABucketNoStepHoldsAPlansKey(t *testing.T) {
	spec, root := fixture(t)

	p, err := Generate(spec, root)
	if err != nil {
		t.Fatal(err)
	}

	if got := holdersOf(t, p, plugin.EnvPlansAccessKeyID); len(got) > 0 {
		t.Errorf("held by %v", got)
	}
}
