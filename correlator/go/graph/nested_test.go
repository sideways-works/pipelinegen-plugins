package graph

import (
	"slices"
	"testing"
)

func TestNestedModulesBothNamed(t *testing.T) {
	root := t.TempDir()
	write(t, root, "backend/go.mod", "module example.com/backend\n\ngo 1.24\n\nrequire example.com/x v0.0.0\n\nreplace example.com/x => ./third_party/x\n")
	write(t, root, "backend/a/a.go", "package a\n\nimport \"example.com/x\"\n\nfunc A() int { return x.X() }\n")
	write(t, root, "backend/third_party/x/go.mod", "module example.com/x\n\ngo 1.24\n")
	write(t, root, "backend/third_party/x/x.go", "package x\n\nconst two = 2\n\nfunc X() int { return two }\n")

	tree, err := Load(t.Context(), root, Options{Modules: []string{"backend", "backend/third_party/x"}})
	if err != nil {
		t.Fatal(err)
	}

	var refs []string

	for _, s := range tree.LinkSets(nil) {
		for _, l := range s.Links {
			if l.From == "go:backend/a#func:A" {
				refs = append(refs, l.To)
			}
		}
	}

	if !slices.Contains(refs, "go:backend/third_party/x#func:X") {
		t.Errorf("A links %v, want x.X", refs)
	}
}
