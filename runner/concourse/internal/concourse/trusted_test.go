package concourse

import (
	"fmt"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/sideways-works/pipelinegen/plugin"
)

type trustedSeen struct {
	tool, repo map[string]string
	// repoTasks counts the repository's own tasks.
	repoTasks int
}

func trustedIn(t *testing.T, out string) trustedSeen {
	t.Helper()

	return paramIn(t, out, plugin.EnvTrusted)
}

func paramIn(t *testing.T, out, name string) trustedSeen {
	t.Helper()

	var doc any
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}

	seen := trustedSeen{tool: map[string]string{}, repo: map[string]string{}}

	var walk func(v any, where string)

	walk = func(v any, where string) {
		switch x := v.(type) {
		case map[string]any:
			if name, ok := x["name"].(string); ok && x["plan"] != nil {
				where = name
			}

			if task, ok := x["task"]; ok {
				at := fmt.Sprintf("%s/%v", where, task)

				if cfg, ok := x["config"].(map[string]any); ok {
					run := fmt.Sprint(cfg["run"])
					params, _ := cfg["params"].(map[string]any) //nolint:errcheck // none: nothing given

					if strings.Contains(run, "tools/pipelinegen") {
						seen.tool[at] = fmt.Sprint(params[name])
					}
				} else if _, ok := x["file"]; ok {
					seen.repoTasks++

					params, _ := x["params"].(map[string]any) //nolint:errcheck // none: nothing given
					if v, ok := params[name]; ok {
						seen.repo[at] = fmt.Sprint(v)
					}
				}
			}

			for _, v := range x {
				walk(v, where)
			}
		case []any:
			for _, e := range x {
				walk(e, where)
			}
		}
	}

	walk(doc, "")

	return seen
}

func TestEveryStepOfTheToolsGetsTheTrustedKeys(t *testing.T) {
	const keys = `{"git_remote":"https://example.test/r.git","plugins":[{"name":"p","repository":"o/p","commit":"0123456789abcdef0123456789abcdef01234567"}]}`

	for _, selfSet := range []bool{false, true} {
		spec, root := fixture(t)
		spec.CI["self_set"] = selfSet
		spec.Trusted = keys

		var texts []string

		for range 2 {
			p, err := Generate(spec, root)
			if err != nil {
				t.Fatal(err)
			}

			texts = append(texts, text(t, p))
		}

		if texts[0] != texts[1] {
			t.Fatal("the same spec gave two pipelines")
		}

		seen := trustedIn(t, texts[0])
		if len(seen.tool) == 0 || seen.repoTasks == 0 {
			t.Fatalf("tasks that run the tool: %d, the repository's: %d", len(seen.tool), seen.repoTasks)
		}

		for at, got := range seen.tool {
			if got != keys {
				t.Errorf("self_set %v: %s gets %q", selfSet, at, got)
			}
		}

		for at := range seen.repo {
			t.Errorf("self_set %v: the repository's task %s gets the trusted keys", selfSet, at)
		}

		spec.Trusted = ""

		p, err := Generate(spec, root)
		if err != nil {
			t.Fatal(err)
		}

		if strings.Contains(text(t, p), plugin.EnvTrusted) {
			t.Errorf("self_set %v: a spec without trusted keys gave a pipeline with them", selfSet)
		}
	}
}

// A planning step holds no link to its job, so it pushes no plugin answers.
func TestEveryStepOfTheToolsIsMarkedAPipelinesStep(t *testing.T) {
	for _, selfSet := range []bool{false, true} {
		spec, root := fixture(t)
		spec.CI["self_set"] = selfSet

		p, err := Generate(spec, root)
		if err != nil {
			t.Fatal(err)
		}

		out := text(t, p)

		seen := paramIn(t, out, plugin.EnvPipelineStep)
		if len(seen.tool) == 0 || seen.repoTasks == 0 {
			t.Fatalf("tasks that run the tool: %d, the repository's: %d", len(seen.tool), seen.repoTasks)
		}

		planning := 0

		for at, got := range seen.tool {
			if got != "1" {
				t.Errorf("self_set %v: %s gets %q", selfSet, at, got)
			}

			if strings.HasPrefix(at, "plan-") {
				planning++
			}
		}

		if planning == 0 {
			t.Errorf("self_set %v: no planning task runs the tool", selfSet)
		}

		for at := range seen.repo {
			t.Errorf("self_set %v: the repository's task %s is marked as the tool's step", selfSet, at)
		}

		for at, url := range paramIn(t, out, "PIPELINEGEN_JOB_URL").tool {
			if strings.HasPrefix(at, "plan-") && url != "<nil>" {
				t.Errorf("self_set %v: the planning task %s holds its job's link %s", selfSet, at, url)
			}
		}
	}
}
