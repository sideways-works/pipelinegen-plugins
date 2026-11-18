package concourse

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestEachStepGetsTheSSHKeyOfItsPurpose(t *testing.T) {
	spec, root := fixture(t)
	spec.GitHub.Tokens = map[string]string{"read": "r", "changes": "c", "report": "p", "store": "s"}
	spec.SSHKeys = map[string]string{"read": "ssh_read", "store": "ssh_store"}

	p, err := Generate(spec, root)
	if err != nil {
		t.Fatal(err)
	}

	var doc any
	if err := yaml.Unmarshal([]byte(text(t, p)), &doc); err != nil {
		t.Fatal(err)
	}

	tasks, repo := 0, 0

	var walk func(v any, where string)

	walk = func(v any, where string) {
		switch x := v.(type) {
		case map[string]any:
			if name, ok := x["name"].(string); ok && x["plan"] != nil {
				where = name
			}

			if task, ok := x["task"]; ok {
				params, _ := x["params"].(map[string]any) //nolint:errcheck // none: nothing given
				if cfg, ok := x["config"].(map[string]any); ok {
					params, _ = cfg["params"].(map[string]any) //nolint:errcheck // none: nothing given
				}

				run := ""
				if cfg, ok := x["config"].(map[string]any); ok {
					run, _ = yamlText(cfg["run"]) //nolint:errcheck // a run that won't marshal names no plan
				} else {
					repo++
				}

				for _, purpose := range []string{"read", "store"} {
					env, keyEnv := "PIPELINEGEN_"+strings.ToUpper(purpose)+"_TOKEN", "PIPELINEGEN_SSH_KEY_"+strings.ToUpper(purpose)
					_, token := params[env]
					key, has := params[keyEnv]

					switch {
					case x["config"] == nil && has:
						t.Errorf("%s/%v: the repository's task holds %s", where, task, keyEnv)
					case token && key != "((ssh_"+purpose+"))":
						t.Errorf("%s/%v: holds the %s token with key %v", where, task, purpose, key)
					case has && !token:
						t.Errorf("%s/%v: holds %s without its token", where, task, keyEnv)
					}
				}

				if strings.Contains(run, "--out plan") {
					if _, ok := params["PIPELINEGEN_SSH_KEY_STORE"]; ok {
						t.Errorf("%s/%v: plans the change's tree holding the store key", where, task)
					}
				}

				tasks++
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

	if tasks == 0 || repo == 0 {
		t.Fatalf("tasks %d, the repository's %d", tasks, repo)
	}

	if out := text(t, p); strings.Contains(out, "PRIVATE KEY") {
		t.Error("a key in the pipeline")
	}
}

func yamlText(v any) (string, error) {
	out, err := yaml.Marshal(v)

	return string(out), err
}
