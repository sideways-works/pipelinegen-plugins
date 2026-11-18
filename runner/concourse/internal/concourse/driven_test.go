package concourse

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func driven(t *testing.T) (plugin.Spec, string) {
	t.Helper()

	spec, root := fixture(t)
	spec.Trusted = `{"git_remote":"x"}`

	i := suiteIndex(t, spec, "backend")
	spec.Suites[i].Prepare, spec.Suites[i].RunSlice = "", ""
	spec.Suites[i].Image = "golang"
	spec.Suites[i].Steps = &plugin.SpecSteps{Prepare: true, PrepareImage: "builder", Drivers: []string{"go"}}

	return spec, root
}

func TestASuiteRunByDriversRunsTheToolsOwnTasks(t *testing.T) {
	spec, root := driven(t)

	if !slices.Contains(describe().Capabilities, plugin.CapSteps) {
		t.Fatal("the runner doesn't say it writes a steps suite's tasks")
	}

	p, err := Generate(spec, root)
	if err != nil {
		t.Fatal(err)
	}

	found := map[string]M{}

	for _, j := range asSlice(p["jobs"]) {
		job := asMap(j)
		if !strings.Contains(fmt.Sprint(job["name"]), "backend") {
			continue
		}

		walkTasks(job, func(task M) {
			if name := fmt.Sprint(task["task"]); name == "batch" || name == "prepare" {
				found[name] = task
			}
		})
	}

	for name, want := range map[string]struct {
		image string
		args  []string
	}{
		"batch":   {"golang", []string{"slice", "run", "--root", "source", "--prepared", "prepared"}},
		"prepare": {"builder", []string{"slice", "prepare", "--root", "source"}},
	} {
		task, ok := found[name]
		if !ok {
			t.Fatalf("no %s task", name)
		}

		cfg := asMap(task["config"])
		run := asMap(cfg["run"])

		if task["file"] != nil || run["path"] != "tools/pipelinegen" || fmt.Sprint(run["args"]) != fmt.Sprint(want.args) {
			t.Errorf("%s runs %v %v (file %v)", name, run["path"], run["args"], task["file"])
		}

		if task["image"] != want.image {
			t.Errorf("%s runs in %v, want %s", name, task["image"], want.image)
		}

		params := asMap(cfg["params"])

		// The tool reads the suite's steps from the trusted keys; the tests get no
		// token.
		if params[plugin.EnvTrusted] != spec.Trusted {
			t.Errorf("%s lacks the trusted keys: %v", name, params)
		}

		for k, v := range params {
			if strings.Contains(k, "TOKEN") || strings.Contains(fmt.Sprint(v), "((github") {
				t.Errorf("%s gets %s", name, k)
			}
		}

		if !slices.ContainsFunc(asSlice(cfg["caches"]), func(c any) bool { return asMap(c)["path"] == "cache" }) {
			t.Errorf("%s keeps no cache", name)
		}
	}
}

func TestASuiteRunByDriversNamesItsImage(t *testing.T) {
	spec, root := driven(t)
	spec.Suites[suiteIndex(t, spec, "backend")].Image = ""

	if _, err := Generate(spec, root); !errors.Is(err, ErrSteps) {
		t.Fatalf("a steps suite without an image: %v", err)
	}
}
