package concourse

import (
	"fmt"
	"slices"
	"testing"
)

func fetched(v any, out map[string][]string) {
	switch x := v.(type) {
	case M:
		if name, ok := x["get"].(string); ok {
			out[name] = tagsOf(x)
		}

		for _, k := range []string{"plan", "in_parallel", "do", "ensure", "on_error", "on_abort", "try"} {
			fetched(x[k], out)
		}
	case []any:
		for _, e := range x {
			fetched(e, out)
		}
	}
}

func tagsOf(step M) []string {
	var tags []string
	for _, t := range asSlice(step["tags"]) {
		tags = append(tags, fmt.Sprint(t))
	}

	return tags
}

func everyTask(v any, f func(M)) {
	switch x := v.(type) {
	case M:
		if x["task"] != nil {
			f(x)
		}

		for _, val := range x {
			everyTask(val, f)
		}
	case []any:
		for _, e := range x {
			everyTask(e, f)
		}
	}
}

// Without peer-to-peer streaming, a volume crossing workers goes through the
// web node.
func TestEveryTaskReadsGetsFromItsOwnWorkers(t *testing.T) {
	for name, bookkeeping := range map[string]any{
		"with the suite": nil, "untagged": []any{}, "own workers": []any{"small"}, "the suite's own": []any{"browsers"},
	} {
		spec, root := fixture(t)

		var first []any
		if bookkeeping != nil {
			first = append(first, map[string]any{"for": "bookkeeping", "tags": bookkeeping})
		}

		p := placed(t, spec, root, first...)

		for _, j := range asSlice(p["jobs"]) {
			job := asMap(j)
			gets := map[string][]string{}
			fetched(job, gets)

			everyTask(job, func(task M) {
				own := tagsOf(task)
				mapped := asMap(task["input_mapping"])

				check := func(what, artifact string) {
					if from, ok := mapped[artifact].(string); ok {
						artifact = from
					}

					if tags, ok := gets[artifact]; ok && !slices.Equal(tags, own) {
						t.Errorf("%s: %s %v: %s %s was fetched with tags %v, the task has %v",
							name, job["name"], task["task"], what, artifact, tags, own)
					}
				}

				if img, ok := task["image"].(string); ok {
					check("image", img)
				}

				for _, in := range asSlice(asMap(task["config"])["inputs"]) {
					check("input", fmt.Sprint(asMap(in)["name"]))
				}
			})
		}

		if bookkeeping == nil {
			gets := map[string][]string{}
			fetched(jobNamed(t, p, "pr-e2e"), gets)

			if _, ok := gets[triggerBookkeeping]; ok {
				t.Errorf("%s: a second get of the trigger", name)
			}
		}
	}
}
