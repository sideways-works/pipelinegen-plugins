package concourse

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func batchTask(t *testing.T, p M, job string) M {
	t.Helper()

	var batch M

	walkMaps(jobByName(t, p, job), func(m M) {
		if m["task"] == "batch" && batch == nil {
			batch = m
		}
	})

	if batch == nil {
		t.Fatalf("%s has no batch task", job)
	}

	return batch
}

func TestAStepMayWaitForAWorkerOnTopOfItsLimit(t *testing.T) {
	for _, c := range []struct {
		name          string
		conf          map[string]any
		batch, ending string
	}{
		{"the default", nil, "550m", "495m"},
		{"a wait of its own", map[string]any{"worker_wait_minutes": 30}, "100m", "45m"},
		{"no wait", map[string]any{"worker_wait_minutes": 0}, "70m", "15m"},
	} {
		t.Run(c.name, func(t *testing.T) {
			spec, root := fixture(t)
			spec.Limits = plugin.SpecLimits{Steps: 15}

			for i := range spec.Runs {
				for j := range spec.Runs[i].Suites {
					spec.Runs[i].Suites[j].Minutes = 70
				}
			}

			p, err := GenerateWith(spec, root, c.conf)
			if err != nil {
				t.Fatal(err)
			}

			batch := batchTask(t, p, "pr-e2e")
			if got := batch["timeout"]; got != c.batch {
				t.Errorf("the batch's limit is %v, want %s", got, c.batch)
			}

			var end M

			walkMaps(jobByName(t, p, "pr-e2e"), func(m M) {
				if m["task"] == "batch-end" {
					end = m
				}
			})

			if got := asMap(asMap(end["config"])["params"])[plugin.EnvLimit]; got != "limits.steps=15" {
				t.Errorf("the batch's end is told %v, the limit without the wait", got)
			}

			if got := end["timeout"]; got != c.ending {
				t.Errorf("the batch's end's limit is %v, want %s", got, c.ending)
			}
		})
	}
}

func TestAnIncludedJobKeepsItsOwnLimits(t *testing.T) {
	spec, root := fixture(t)

	if err := os.WriteFile(filepath.Join(root, "root.yml"), []byte("jobs:\n- name: title\n  plan:\n  - task: hello\n    timeout: 5m\n    file: x.yml\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	spec.CI["include"] = "root.yml"

	p, err := Generate(spec, root)
	if err != nil {
		t.Fatal(err)
	}

	var got any

	walkMaps(jobByName(t, p, "title"), func(m M) {
		if m["task"] == "hello" {
			got = m["timeout"]
		}
	})

	if got != "5m" {
		t.Errorf("the included task's limit became %v", got)
	}
}

func TestANegativeWorkerWaitIsRefused(t *testing.T) {
	spec, root := fixture(t)

	_, err := GenerateWith(spec, root, map[string]any{"worker_wait_minutes": -1})
	if !errors.Is(err, ErrPlacement) {
		t.Fatalf("got %v", err)
	}
}

func TestAStepInTwoPlacesTakesTheWaitOnce(t *testing.T) {
	step := M{"task": "batch", "timeout": "70m", "config": M{"params": M{"timeout": "1m"}}}
	jobs := []any{M{"plan": []any{step, M{"try": step}}}}

	waitForWorkers(jobs, 30)

	if step["timeout"] != "100m" {
		t.Errorf("the step's limit is %v", step["timeout"])
	}

	if got := asMap(asMap(step["config"])["params"])["timeout"]; got != "1m" {
		t.Errorf("a param named timeout became %v", got)
	}
}
