package concourse

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

// concourseName is what `fly set-pipeline --strict` takes as a name.
var concourseName = regexp.MustCompile(`^[\p{Ll}\p{Lt}\p{Lm}\p{Lo}][\p{Ll}\p{Lt}\p{Lm}\p{Lo}\d\-_.]*$`)

func TestNamesSafeForAnyBranch(t *testing.T) {
	spec, root := fixture(t)

	main := runIndex(t, spec, "main")
	spec.Runs[main].Trigger.Branch = "ci/pipelinegen-adoption-2"
	nightly := runIndex(t, spec, "nightly")
	spec.Runs[nightly].Trigger.Branch = "ci_2fpipelinegen-adoption-2"

	p, err := Generate(spec, root)
	if err != nil {
		t.Fatal(err)
	}

	used := map[string]bool{}

	var walk func(v any)

	walk = func(v any) {
		switch x := v.(type) {
		case M:
			for _, k := range []string{"get", "put"} {
				if name, ok := x[k].(string); ok {
					used[name] = true
					if r, ok := x["resource"].(string); ok {
						used[r] = true
						delete(used, name)
					}
				}
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

	walk(p["jobs"])

	seen := map[string]bool{}

	for _, kind := range []string{"resources", "jobs"} {
		for _, it := range asSlice(p[kind]) {
			name := fmt.Sprint(asMap(it)["name"])
			if !concourseName.MatchString(name) {
				t.Errorf("%s %q: no name Concourse takes", kind, name)
			}

			if seen[name] {
				t.Errorf("%s %q twice", kind, name)
			}

			seen[name] = true

			if kind == "resources" && !used[name] {
				t.Errorf("resource %s: no job uses it", name)
			}
		}
	}

	if !seen["branch-ci_2fpipelinegen-adoption-2"] || !seen["branch-ci_5f2fpipelinegen-adoption-2"] {
		t.Errorf("branch resources: %v", seen)
	}

	only := spec
	only.Runs = []plugin.SpecRun{spec.Runs[runIndex(t, spec, "pr")]}

	if p, err = Generate(only, root); err != nil {
		t.Fatal(err)
	}

	var branches []string

	for _, it := range asSlice(p["resources"]) {
		if name := fmt.Sprint(asMap(it)["name"]); strings.HasPrefix(name, "branch-") {
			branches = append(branches, name)
		}
	}

	if fmt.Sprint(branches) != "[branch-main]" {
		t.Errorf("branch resources of pull request runs alone: %v", branches)
	}
}
