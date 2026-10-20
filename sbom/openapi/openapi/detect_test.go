package openapi

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestDetect(t *testing.T) {
	dir := tree(t)

	others := map[string]string{
		"config/app.yaml":  "name: openapi tools\npaths: [a, b]\n",
		"broken.yaml":      "openapi: [\n",
		"pkg.json":         `{"name": "swagger-ui"}`,
		"partial.json":     `{"openapi": "3.1.0", "info": {}}`,
		"gen/codegen.yaml": "package: gen\noutput: openapi.gen.go\n",
	}
	for f, s := range others {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(f)), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(dir, f), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	candidates, err := Candidates(dir)
	if err != nil {
		t.Fatal(err)
	}

	found := Detect(dir, candidates)

	var got []string
	for _, d := range found {
		got = append(got, d.Path+" "+d.Role)
	}

	if want := []string{"api.yaml contract", "hooks.yaml contract"}; !slices.Equal(got, want) {
		t.Errorf("detected %q, want %q", got, want)
	}

	if !slices.Equal(found[0].References, []string{"common.yaml"}) {
		t.Errorf("api.yaml references %q", found[0].References)
	}

	var docs []string
	for _, d := range Documents(found) {
		docs = append(docs, d.Path)
	}

	if want := []string{"api.yaml", "common.yaml", "hooks.yaml"}; !slices.Equal(docs, want) {
		t.Errorf("documents %q, want %q", docs, want)
	}
}
