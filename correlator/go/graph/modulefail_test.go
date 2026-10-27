package graph

import (
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen-sbom-go/decls"
)

func repo(t *testing.T, files map[string]string) string {
	t.Helper()

	root := t.TempDir()
	for name, src := range files {
		write(t, root, name, src)
	}

	return root
}

// appFiles are a module with two independent packages, a and b, and a
// second module, tools, with no Go file at its root.
var appFiles = map[string]string{
	"go.mod":            "module example.com/app\n\ngo 1.24\n",
	"a/a.go":            "package a\n\n// A is one.\nfunc A() int { return 1 }\n",
	"a/a_test.go":       "package a\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) { _ = A() }\n",
	"b/b.go":            "package b\n\n// B is two.\nfunc B() int { return 2 }\n",
	"tools/go.mod":      "module example.com/tools\n\ngo 1.24\n",
	"tools/gen/main.go": "package main\n\n// Gen is three.\nfunc Gen() int { return 3 }\n\nfunc main() { _ = Gen() }\n",
}

func with(extra map[string]string) map[string]string {
	out := maps.Clone(appFiles)
	maps.Copy(out, extra)

	return out
}

func setOf(tr *Tree, dir string) LinkSet {
	for _, s := range tr.LinkSets([]string{dir}) {
		return s
	}

	return LinkSet{}
}

func TestModuleRootWithoutGoFiles(t *testing.T) {
	root := repo(t, with(map[string]string{"go.work": "go 1.24\n\nuse (\n\t.\n\t./tools\n)\n"}))

	for _, o := range []Options{{}, {Modules: []string{".", "tools"}, Packages: []string{"a"}}, {Modules: []string{".", "tools"}, Packages: []string{"tools/gen"}}} {
		tr, err := Load(t.Context(), root, o)
		if err != nil {
			t.Fatalf("%+v: %v", o, err)
		}

		if len(tr.Broken()) > 0 {
			t.Errorf("%+v: broken %v", o, tr.Broken())
		}
	}
}

func TestModuleFailures(t *testing.T) {
	clean, err := Load(t.Context(), repo(t, appFiles), Options{})
	if err != nil {
		t.Fatal(err)
	}

	for name, c := range map[string]struct {
		files       map[string]string
		build       decls.Build
		env         map[string]string
		broken      []string
		says        string
		uncacheable bool
	}{
		"a module asking for a newer go": {
			files:  map[string]string{"tools/go.mod": "module example.com/tools\n\ngo 1.99\n"},
			broken: []string{"tools/gen"}, says: "tools/go.mod asks for go1.99, the go command run there is go", uncacheable: true,
		},
		"a replace to a missing directory": {
			files: map[string]string{
				"go.mod": "module example.com/app\n\ngo 1.24\n\nrequire example.com/gone v0.0.0\n\nreplace example.com/gone => ./gone\n",
				"b/b.go": "package b\n\nimport \"example.com/gone\"\n\n// B is two.\nfunc B() int { return gone.Two }\n",
			},
			broken: []string{"a", "b"}, says: "could not import example.com/gone",
		},
		"inconsistent vendoring": {
			files: map[string]string{
				"go.mod":             "module example.com/app\n\ngo 1.24\n\nrequire example.com/dep v1.0.0\n",
				"vendor/modules.txt": "# example.com/other v1.0.0\n## explicit\nexample.com/other\n",
			},
			broken: []string{"a", "b"}, says: "go: inconsistent vendoring in .",
		},
		"cgo without a C compiler": {
			files:  map[string]string{"b/c.go": "package b\n\n// int seven() { return 7; }\nimport \"C\"\n\n// Seven calls C.\nfunc Seven() int { return int(C.seven()) }\n"},
			build:  decls.Build{Cgo: true},
			env:    map[string]string{"CC": "no-such-cc"},
			broken: []string{"a", "b"}, says: "b/c.go:4:8: could not import C", uncacheable: true,
		},
		"a nested module": {
			files:  map[string]string{"b/plug/go.mod": "module example.com/plug\n\ngo 1.24\n", "b/plug/p.go": "package plug\n\n// P is four.\nfunc P() int { return 4 }\n"},
			broken: nil,
		},
	} {
		t.Run(name, func(t *testing.T) {
			for k, v := range c.env {
				t.Setenv(k, v)
			}

			root := repo(t, with(c.files))

			tr, err := Load(t.Context(), root, Options{Build: c.build})
			if err != nil {
				t.Fatalf("the load fails: %v", err)
			}

			got := tr.Broken()
			if dirs := sortedKeys(got); !slices.Equal(dirs, c.broken) {
				t.Errorf("broken %q, want %q", dirs, c.broken)
			}

			for dir, why := range got {
				if !strings.Contains(why, c.says) || strings.Contains(why, root) || strings.Contains(why, "/home/") {
					t.Errorf("%s: %q, want it to say %q from the tree's root", dir, why, c.says)
				}
			}

			if (tr.Uncacheable != "") != c.uncacheable {
				t.Errorf("uncacheable %q, want %v", tr.Uncacheable, c.uncacheable)
			}

			// what the failure doesn't touch stays exact: every package
			// that is neither broken nor edited by the case
			edited := c.files["b/b.go"] != "" || c.files["b/c.go"] != ""

			for _, dir := range []string{"a", "b", "tools/gen"} {
				if _, hit := got[dir]; hit || (dir == "b" && edited) {
					continue
				}

				if !reflect.DeepEqual(setOf(tr, dir), setOf(clean, dir)) {
					t.Errorf("%s's links changed:\n got %+v\nwant %+v", dir, setOf(tr, dir), setOf(clean, dir))
				}
			}
		})
	}
}
