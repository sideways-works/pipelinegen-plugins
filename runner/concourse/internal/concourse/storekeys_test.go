package concourse

import (
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
	"go.yaml.in/yaml/v3"
)

// A store plugin's writing key goes to every step that holds the store token, the reading key to the
// steps that plan a proposed change's tree, and neither to a task of the repository's.
func TestEachStepGetsTheStoresKeyItsRecordsNeed(t *testing.T) {
	for _, split := range []bool{true, false} {
		spec, root := fixture(t)
		if split {
			spec.GitHub.Tokens = map[string]string{"read": "r", "changes": "c", "report": "p", "store": "s"}
		}

		spec.Store = &plugin.SpecStore{
			Plugin: "s3",
			Write:  plugin.SpecKey{AccessKeyID: "store_id", SecretAccessKey: "store_secret"},
			Read:   &plugin.SpecKey{AccessKeyID: "store_read_id", SecretAccessKey: "store_read_secret"}, //nolint:gosec // var names, not credentials
		}

		p, err := Generate(spec, root)
		if err != nil {
			t.Fatal(err)
		}

		var doc any
		if err := yaml.Unmarshal([]byte(text(t, p)), &doc); err != nil {
			t.Fatal(err)
		}

		writing, reading, repo := 0, 0, 0

		for _, task := range tasksOf(doc, "") {
			id := task.params[plugin.EnvStoreAccessKeyID]
			_, secret := task.params[plugin.EnvStoreSecretAccessKey]
			_, token := task.params["PIPELINEGEN_STORE_TOKEN"]

			if (id != nil) != secret {
				t.Errorf("%s: holds half a key", task.where)
			}

			switch {
			case !task.tool:
				repo++

				if id != nil {
					t.Errorf("%s: the repository's task holds the store's key", task.where)
				}
			case id == "((store_read_id))":
				reading++

				if split && token {
					t.Errorf("%s: holds the store token with the reading key", task.where)
				}
			case id == "((store_id))":
				writing++

				if split && !token {
					t.Errorf("%s: holds the writing key without the store token", task.where)
				}
			case id != nil:
				t.Errorf("%s: holds %v", task.where, id)
			case split && token:
				t.Errorf("%s: holds the store token without the writing key", task.where)
			}
		}

		// the steps that plan a proposed change's tree, of its planning and its sweep
		if writing == 0 || reading != 2 || repo == 0 {
			t.Fatalf("split %v: writing %d, reading %d, the repository's %d", split, writing, reading, repo)
		}
	}
}

type taskParams struct {
	where  string
	tool   bool
	params map[string]any
}

// tasksOf lists each task of the pipeline with its job and its params.
func tasksOf(v any, where string) []taskParams {
	var out []taskParams

	switch x := v.(type) {
	case map[string]any:
		if name, ok := x["name"].(string); ok && x["plan"] != nil {
			where = name
		}

		if task, ok := x["task"].(string); ok {
			t := taskParams{where: where + "/" + task}

			t.params, _ = x["params"].(map[string]any) //nolint:errcheck // none: nothing given
			if cfg, ok := x["config"].(map[string]any); ok {
				t.tool = true
				t.params, _ = cfg["params"].(map[string]any) //nolint:errcheck // none: nothing given
			}

			out = append(out, t)
		}

		for _, e := range x {
			out = append(out, tasksOf(e, where)...)
		}
	case []any:
		for _, e := range x {
			out = append(out, tasksOf(e, where)...)
		}
	}

	return out
}
