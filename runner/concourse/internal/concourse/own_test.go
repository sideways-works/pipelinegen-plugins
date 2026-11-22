package concourse

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/sideways-works/pipelinegen/plugin"
)

// testdata/own/before.yml was written from a spec that carried worker tags;
// request.json places them in the runner's config. Only the trusted keys and
// the pipeline's hash of itself may differ.
func TestOwnPipelineMovedIsTheSame(t *testing.T) {
	dir := filepath.Join("testdata", "own")

	data, err := os.ReadFile(filepath.Join(dir, "request.json"))
	if err != nil {
		t.Fatal(err)
	}

	var req struct {
		Spec   plugin.Spec    `json:"spec"`
		Config map[string]any `json:"config"`
	}

	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatal(err)
	}

	// before.yml was written before a step's limit had the wait for a worker on top
	req.Config["worker_wait_minutes"] = 0

	p, err := GenerateWith(req.Spec, dir, req.Config)
	if err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(filepath.Join(dir, "before.yml"))
	if err != nil {
		t.Fatal(err)
	}

	got, want := movedOnly(t, []byte(text(t, p)), true), movedOnly(t, before, false)

	if got != want {
		g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
		for i := range min(len(g), len(w)) {
			if g[i] != w[i] {
				t.Fatalf("the pipeline after the move differs at line %d:\n got: %s\nwant: %s", i+1, g[i], w[i])
			}
		}

		t.Fatalf("the pipeline after the move has %d lines, before %d", len(g), len(w))
	}
}

func movedOnly(t *testing.T, text []byte, after bool) string {
	t.Helper()

	var doc any
	if err := yaml.Unmarshal(text, &doc); err != nil {
		t.Fatal(err)
	}

	var trusted []string

	var walk func(v any)

	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if _, ok := x[staleHash]; ok {
				x[staleHash] = ""
			}

			if blob, ok := x[plugin.EnvTrusted].(string); ok {
				var keys map[string]any
				if err := json.Unmarshal([]byte(blob), &keys); err != nil {
					t.Fatalf("trusted keys: %v", err)
				}

				if _, has := keys["runner_config"]; has != after {
					t.Fatalf("the runner's block in the trusted keys: %v, want %v", has, after)
				}

				delete(keys, "runner_config")

				out, err := json.Marshal(keys)
				if err != nil {
					t.Fatal(err)
				}

				x[plugin.EnvTrusted] = string(out)
				trusted = append(trusted, string(out))
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

	walk(doc)

	if len(trusted) == 0 || !reflect.DeepEqual(trusted[0], trusted[len(trusted)-1]) {
		t.Fatal("no trusted keys, or different ones in one pipeline")
	}

	out, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}

	return string(out)
}
