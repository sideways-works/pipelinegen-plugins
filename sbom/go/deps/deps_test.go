package deps

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen-plugins/sbom/go/goid"
)

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()

	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

const appMod = `module example.com/app

go 1.25

toolchain go1.25.3

require (
	example.com/a v1.2.0
	example.com/b v1.0.0 // indirect
	example.com/c v0.1.0
	example.com/d v1.0.0
	example.com/tools v0.0.0
)

replace example.com/c => ./c

replace example.com/d => example.com/dfork v1.1.0
`

const appSum = `example.com/a v1.2.0 h1:AAA=
example.com/a v1.2.0/go.mod h1:AAAmod=
example.com/b v1.0.0 h1:BBB=
example.com/dfork v1.1.0 h1:DDD=
`

func repo(t *testing.T) (root, cache string) {
	t.Helper()

	root, cache = t.TempDir(), t.TempDir()
	write(t, root, map[string]string{
		"app/go.mod":   appMod,
		"app/go.sum":   appSum,
		"app/c/go.mod": "module example.com/c\n",
		"tools/go.mod": "module example.com/tools\n\ngo 1.25\n",
		"MODULE.bazel": "go_sdk = use_extension(\"@rules_go//go:extensions.bzl\", \"go_sdk\")\ngo_sdk.download(version = \"1.25.3\")\n",
	})
	write(t, cache, map[string]string{
		"cache/download/example.com/a/@v/v1.2.0.mod": "module example.com/a\n\nrequire example.com/b v1.0.0\n",
		"cache/download/example.com/b/@v/v1.0.0.mod": "module example.com/b\n",
	})

	return root, cache
}

func read(t *testing.T, root, cache string, mods ...string) *Graph {
	t.Helper()

	if mods == nil {
		mods = []string{"app"}
	}

	g, err := Read(root, mods, cache)
	if err != nil {
		t.Fatal(err)
	}

	return g
}

func byID(g *Graph) map[string]Module {
	out := map[string]Module{}
	for _, m := range g.Modules {
		out[m.ID] = m
	}

	return out
}

func TestModules(t *testing.T) {
	root, cache := repo(t)
	g := read(t, root, cache)
	got := byID(g)

	if ids := slices.Sorted(maps.Keys(got)); !slices.Equal(ids, []string{"dep:go:example.com/a", "dep:go:example.com/b", "dep:go:example.com/d"}) {
		t.Errorf("units %v: want a, b and d; the local replace and the first-party module have none", ids)
	}

	if a := got["dep:go:example.com/a"]; a.Version != "v1.2.0" || a.Sum != "h1:AAA=" || a.Indirect {
		t.Errorf("a: %+v", a)
	}

	if b := got["dep:go:example.com/b"]; !b.Indirect {
		t.Errorf("b isn't indirect: %+v", b)
	}

	if d := got["dep:go:example.com/d"]; d.Replace != "example.com/dfork@v1.1.0" || d.Sum != "h1:DDD=" {
		t.Errorf("d keeps its id with the replacement in the digest: %+v", d)
	}

	if !slices.Equal(g.Links, []Link{{From: "dep:go:example.com/a", To: "dep:go:example.com/b"}}) {
		t.Errorf("links %v", g.Links)
	}

	if len(g.CannotTell) != 1 || g.CannotTell[0].ID != "dep:go:example.com/d" || !strings.Contains(g.CannotTell[0].Reason, "dfork@v1.1.0") {
		t.Errorf("cannot tell %v: want d, whose replacement's go.mod isn't cached", g.CannotTell)
	}

	tools := map[string]string{}
	for _, tl := range g.Tools {
		tools[tl.ID] = tl.Version
	}

	if !maps.Equal(tools, map[string]string{goid.GoVersion: "1.25", goid.Toolchain: "go1.25.3", goid.BazelSDK: "1.25.3"}) {
		t.Errorf("tools %v", tools)
	}

	for _, in := range []string{"app/go.mod", "app/go.sum", "MODULE.bazel", "tools/go.mod"} {
		if !slices.Contains(g.Inputs, in) {
			t.Errorf("inputs %v lack %s", g.Inputs, in)
		}
	}
}

func digests(g *Graph) map[string]string {
	out := map[string]string{}
	for _, m := range g.Modules {
		out[m.ID] = m.Digest
	}

	for _, tl := range g.Tools {
		out[tl.ID] = tl.Digest
	}

	return out
}

func changed(t *testing.T, edit map[string]string, mods ...string) []string {
	t.Helper()

	root, cache := repo(t)
	base := digests(read(t, root, cache, mods...))

	write(t, root, edit)
	head := digests(read(t, root, cache, mods...))

	var out []string

	for id, d := range head {
		if base[id] != d {
			out = append(out, id)
		}
	}

	for id := range base {
		if _, ok := head[id]; !ok {
			out = append(out, id+" (gone)")
		}
	}

	slices.Sort(out)

	return out
}

func TestDigests(t *testing.T) {
	cases := map[string]struct {
		edit map[string]string
		want []string
	}{
		"a tidy that adds only go.mod lines changes nothing": {
			edit: map[string]string{"app/go.sum": appSum + "example.com/b v1.0.0/go.mod h1:BBBmod=\nexample.com/x v0.1.0/go.mod h1:XXX=\n"},
		},
		"a bump changes that module only": {
			edit: map[string]string{
				"app/go.mod": strings.Replace(appMod, "example.com/a v1.2.0", "example.com/a v1.3.0", 1),
				"app/go.sum": appSum + "example.com/a v1.3.0 h1:AAA3=\n",
			},
			want: []string{"dep:go:example.com/a"},
		},
		"a new replacement changes the replaced module": {
			edit: map[string]string{"app/go.mod": strings.Replace(appMod, "dfork v1.1.0", "dfork v1.2.0", 1)},
			want: []string{"dep:go:example.com/d"},
		},
		"the go directive changes its toolchain unit only": {
			edit: map[string]string{"app/go.mod": strings.Replace(appMod, "go 1.25\n", "go 1.26\n", 1)},
			want: []string{goid.GoVersion},
		},
		"the Bazel SDK changes its own unit": {
			edit: map[string]string{"MODULE.bazel": "go_sdk.download(version = \"1.26.0\")\n"},
			want: []string{goid.BazelSDK},
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := changed(t, c.edit); !slices.Equal(got, c.want) {
				t.Errorf("changed %v, want %v", got, c.want)
			}
		})
	}
}

func TestWorkspace(t *testing.T) {
	root, cache := t.TempDir(), t.TempDir()
	write(t, root, map[string]string{
		"tools/gowork/go.work": "go 1.25\n\nuse (\n\t../../api\n\t../../infra\n)\n",
		"api/go.mod":           "module example.com/api\n\ngo 1.25\n\nrequire (\n\texample.com/x v1.1.0\n\texample.com/infra v0.0.0\n)\n",
		"api/go.sum":           "example.com/x v1.1.0 h1:X11=\n",
		"infra/go.mod":         "module example.com/infra\n\ngo 1.25\n\nrequire example.com/x v1.4.0\n",
		"infra/go.sum":         "example.com/x v1.4.0 h1:X14=\n",
	})

	g := read(t, root, cache, "api", "infra")
	x := byID(g)["dep:go:example.com/x"]

	if x.Version != "v1.4.0" || x.Sum != "h1:X14=" {
		t.Errorf("x %+v: a workspace builds the highest version its modules require", x)
	}

	if _, ok := byID(g)["dep:go:example.com/infra"]; ok {
		t.Error("a workspace module is a dependency")
	}

	// raising the lower requirement to one still below the highest changes
	// nothing the workspace builds
	write(t, root, map[string]string{"api/go.mod": "module example.com/api\n\ngo 1.25\n\nrequire example.com/x v1.2.0\n"})

	if got := byID(read(t, root, cache, "api", "infra"))["dep:go:example.com/x"]; got.Digest != x.Digest {
		t.Error("a requirement below the selected version changed the digest")
	}
}

func TestSeparateModules(t *testing.T) {
	root, cache := t.TempDir(), t.TempDir()
	write(t, root, map[string]string{
		"api/go.mod":   "module example.com/api\n\nrequire example.com/x v1.1.0\n",
		"infra/go.mod": "module example.com/infra\n\nrequire example.com/x v1.4.0\n",
	})

	base := byID(read(t, root, cache, "api", "infra"))["dep:go:example.com/x"]

	write(t, root, map[string]string{"api/go.mod": "module example.com/api\n\nrequire example.com/x v1.2.0\n"})

	if head := byID(read(t, root, cache, "api", "infra"))["dep:go:example.com/x"]; head.Digest == base.Digest {
		t.Error("without a workspace, each module builds its own version: a bump in one must change the digest")
	}
}

func TestVendor(t *testing.T) {
	root, cache := t.TempDir(), t.TempDir()
	write(t, root, map[string]string{
		"go.mod":             "module example.com/app\n\ngo 1.16\n\nrequire example.com/a v1.0.0\n",
		"vendor/modules.txt": "# example.com/a v1.0.0\n## explicit\nexample.com/a\n# example.com/b v0.3.0\nexample.com/b\n",
	})

	g := read(t, root, cache, ".")
	if b, ok := byID(g)["dep:go:example.com/b"]; !ok || b.Version != "v0.3.0" || !b.Indirect {
		t.Errorf("b from vendor/modules.txt: %+v", b)
	}

	if !slices.Contains(g.Inputs, "vendor/modules.txt") {
		t.Errorf("inputs %v", g.Inputs)
	}
}
