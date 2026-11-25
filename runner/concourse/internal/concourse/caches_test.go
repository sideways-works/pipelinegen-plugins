package concourse

import (
	"errors"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

// cachesOfTasks are the job caches of each task of a job, by the task's name.
func cachesOfTasks(p M, jobName string) map[string][]any {
	out := map[string][]any{}

	for _, j := range asSlice(p["jobs"]) {
		job := asMap(j)
		if job["name"] != jobName {
			continue
		}

		walkSteps(job, func(step M) {
			name, ok := step["task"].(string)
			if !ok {
				return
			}

			if cfg, ok := step["config"].(M); ok {
				out[name] = asSlice(cfg["caches"])
			}
		})
	}

	return out
}

// A suite caches.without names keeps no job cache in its prepare and batch tasks; another suite keeps its.
func TestASuiteWithoutCachesKeepsNoJobCache(t *testing.T) {
	spec, root := fixture(t)

	for _, name := range []string{"e2e", "backend"} {
		i := suiteIndex(t, spec, name)
		spec.Suites[i].Prepare, spec.Suites[i].RunSlice = "", ""
		spec.Suites[i].Image = "golang"
		spec.Suites[i].Steps = &plugin.SpecSteps{Prepare: true, Drivers: []string{"go"}}
	}

	p := generated(t, spec, root, map[string]any{"caches": map[string]any{"without": []any{"e2e"}}})

	for _, task := range []string{"prepare", "batch"} {
		if c, ok := cachesOfTasks(p, "main-e2e")[task]; !ok || len(c) != 0 {
			t.Errorf("e2e's %s: caches %v (found %v)", task, c, ok)
		}

		if c := cachesOfTasks(p, "main-backend")[task]; len(c) != 1 || asMap(c[0])["path"] != "cache" {
			t.Errorf("backend's %s: caches %v", task, c)
		}
	}

	if _, err := GenerateWith(spec, root, map[string]any{"caches": map[string]any{"without": []any{"e2e2"}}}); !errors.Is(err, ErrPlacement) {
		t.Errorf("a name that is no suite: %v", err)
	}
}
