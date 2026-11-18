package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen-plugins/correlator/go/graph"
	"github.com/sideways-works/pipelinegen/plugin"
)

func asked(t *testing.T, root string, paths []string) ([]plugin.Set, string) {
	t.Helper()

	extra := map[string]any{}
	if paths != nil {
		extra["paths"] = paths
	}

	req := request(t, root, extra)

	code, out, errOut := run(t, "links", req)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}

	work, ok := req["work"].(string)
	if !ok {
		t.Fatal("no work directory")
	}

	all := readLines[plugin.Set](t, filepath.Join(work, setsFile))

	return slices.DeleteFunc(all, func(s plugin.Set) bool { return s.Group == graph.NamesGroup }), out
}

func TestLinksAnswerTheNamesSet(t *testing.T) {
	root := fixture(t)

	var got []plugin.Set

	for _, paths := range [][]string{nil, {"shop/orders/orders.go"}, {"shop/go.sum"}} {
		extra := map[string]any{}
		if paths != nil {
			extra["paths"] = paths
		}

		req := request(t, root, extra)
		if code, _, errOut := run(t, "links", req); code != 0 {
			t.Fatalf("exit %d: %s", code, errOut)
		}

		work, _ := req["work"].(string) //nolint:errcheck // request sets it
		for _, s := range readLines[plugin.Set](t, filepath.Join(work, setsFile)) {
			if s.Group == graph.NamesGroup {
				got = append(got, s)
			}
		}
	}

	if len(got) != 3 {
		t.Fatalf("%d answers held the names set, want 3", len(got))
	}

	for _, s := range got[1:] {
		if !reflectEqual(t, s, got[0]) {
			t.Errorf("the names set differs by what was asked:\n%+v\n%+v", s, got[0])
		}
	}

	for _, in := range []string{"shop/go.mod", "shop/orders/orders.go", "shop/orders/create_test.go", "shop/money/money.go"} {
		if !slices.Contains(got[0].Inputs, in) {
			t.Errorf("the names set doesn't read %s: %v", in, got[0].Inputs)
		}
	}
}

func groups(sets []plugin.Set) []string {
	out := []string{}
	for _, s := range sets {
		out = append(out, s.Group)
	}

	return out
}

func TestLinksAnswersTheAskedPackages(t *testing.T) {
	root := fixture(t)

	whole, err := graph.Load(t.Context(), root, graph.Options{})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]plugin.Set{}
	for _, s := range whole.LinkSets(nil) {
		want[s.Group] = setOf(s)
	}

	cases := []struct {
		paths []string
		dirs  []string
	}{
		{[]string{"shop/orders/orders.go"}, []string{"shop/orders"}},
		{[]string{"shop/orders/create_test.go", "shop/money/money.go"}, []string{"shop/money", "shop/orders"}},
		{[]string{"shop/go.mod"}, []string{"shop/cmd/shop", "shop/money", "shop/orders"}},
		{[]string{"shop/go.sum"}, []string{}},
		{nil, []string{"shop/cmd/shop", "shop/money", "shop/orders"}},
	}

	for _, c := range cases {
		sets, out := asked(t, root, c.paths)
		if !strings.Contains(out, `"files":["sets.jsonl"]`) {
			t.Errorf("%v: answer %s", c.paths, out)
		}

		if got := groups(sets); !slices.Equal(got, c.dirs) {
			t.Errorf("%v: sets of %v, want %v", c.paths, got, c.dirs)
		}

		for _, s := range sets {
			if !equalSets(s, want[s.Group]) {
				t.Errorf("%v: %s answers %+v, the whole load %+v", c.paths, s.Group, s, want[s.Group])
			}
		}
	}
}

func equalSets(a, b plugin.Set) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)

	return errA == nil && errB == nil && string(ja) == string(jb)
}

func TestLinksAnswerAGonePackage(t *testing.T) {
	root := fixture(t)

	if err := os.RemoveAll(filepath.Join(root, "shop", "money")); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(root, "shop", "orders", "testdata"), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, "shop", "orders", "testdata", "x.go"), []byte("package x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// cmd/shop imports money and no longer builds, so it isn't asked
	sets, _ := asked(t, root, []string{"shop/money/money.go", "shop/orders/testdata/x.go"})

	if got := groups(sets); !slices.Equal(got, []string{"shop/money"}) {
		t.Fatalf("sets of %v", got)
	}

	for _, s := range sets {
		if s.Inputs == nil || len(s.Inputs)+len(s.InputUnits)+len(s.Links) > 0 {
			t.Errorf("%s answers %+v, want an empty set", s.Group, s)
		}
	}
}

func TestLinksAnswerAGonePackageBesideALoadedOne(t *testing.T) {
	sets, _ := asked(t, fixture(t), []string{"shop/orders/orders.go", "shop/gone/gone.go"})

	if got := groups(sets); !slices.Equal(got, []string{"shop/gone", "shop/orders"}) {
		t.Fatalf("sets of %v", got)
	}

	if s := sets[0]; s.Inputs == nil || len(s.Inputs)+len(s.InputUnits)+len(s.Links) > 0 {
		t.Errorf("shop/gone answers %+v, want an empty set", s)
	}

	if len(sets[1].Links) == 0 {
		t.Error("shop/orders answers no links")
	}
}

func TestLinksAreStable(t *testing.T) {
	root := fixture(t)

	var texts []string

	for range 2 {
		req := request(t, root, nil)
		if code, _, errOut := run(t, "links", req); code != 0 {
			t.Fatalf("exit %d: %s", code, errOut)
		}

		work, ok := req["work"].(string)
		if !ok {
			t.Fatal("no work directory")
		}

		b, err := os.ReadFile(filepath.Join(work, setsFile))
		if err != nil {
			t.Fatal(err)
		}

		texts = append(texts, string(b))
	}

	if texts[0] != texts[1] {
		t.Error("two calls answered different bytes")
	}
}

func TestLinksAreDeclaredPerSet(t *testing.T) {
	_, out, _ := run(t, "describe", nil)

	var d plugin.Description
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatal(err)
	}

	if !slices.Contains(d.Capabilities, callLinks) || d.Inputs[callLinks] != plugin.PerSet {
		t.Errorf("describe doesn't offer links per set: %s", out)
	}
}

func TestLinksAnswerAWorkspace(t *testing.T) {
	root := t.TempDir()

	for rel, src := range map[string]string{
		"go.work":       "go 1.24\n\nuse (\n\t./mods/a\n\t./mods/b\n)\n",
		"mods/a/go.mod": "module example.com/a\n\ngo 1.24\n",
		"mods/a/a.go":   "package a\n\nimport \"example.com/b\"\n\nfunc A() int { return b.B() }\n",
		"mods/b/go.mod": "module example.com/b\n\ngo 1.24\n",
		"mods/b/b.go":   "package b\n\nfunc B() int { return 1 }\n",
	} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	sets, _ := asked(t, root, []string{"go.work"})
	if got := groups(sets); !slices.Equal(got, []string{"mods/a", "mods/b"}) {
		t.Fatalf("sets of %v", got)
	}

	for _, s := range sets {
		if !slices.Contains(s.Inputs, "go.work") {
			t.Errorf("%s reads %v", s.Group, s.Inputs)
		}
	}
}

// brokenShop adds to the shop a package of each kind that doesn't build (all
// files behind a tag, tests that don't type-check, a package that doesn't
// type-check, a file that doesn't parse) and an importer of one of them.
func brokenShop(t *testing.T) string {
	t.Helper()

	root := fixture(t)

	for name, src := range map[string]string{
		"shop/gated/flow_test.go":    "//go:build e2e\n\npackage gated\n\nimport \"testing\"\n\nfunc TestFlow(t *testing.T) {}\n",
		"shop/stale/stale.go":        "package stale\n\n// Use is fine.\nfunc Use() int { return 1 }\n",
		"shop/stale/helpers_test.go": "package stale\n\ntype finder interface{ Find(id int) int }\n\ntype stub struct{}\n\nfunc (stub) Find(id string) int { return len(id) }\n\nvar _ finder = stub{}\n",
		"shop/typed/typed.go":        "package typed\n\n// Bad doesn't type-check.\nfunc Bad() string { return 1 }\n\n// Good does.\nfunc Good() int { return 2 }\n",
		"shop/syntax/syntax.go":      "package syntax\n\nfunc Broken( {\n",
		"shop/via/via.go":            "package via\n\nimport \"example.com/shop/typed\"\n\n// Via calls into typed.\nfunc Via() int { return typed.Good() }\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return root
}

func TestLinksOfATreeWithBrokenPackages(t *testing.T) {
	root := brokenShop(t)

	sets, _ := asked(t, root, nil)
	byGroup := map[string]plugin.Set{}

	for _, s := range sets {
		byGroup[s.Group] = s
	}

	for _, g := range []string{"shop/gated", "shop/stale", "shop/syntax", "shop/typed"} {
		s := byGroup[g]
		if len(s.CannotTell) == 0 || !strings.HasPrefix(s.CannotTell[0].Reason, "package "+g+" does not build with tags [pipelinegen_cover]: ") {
			t.Errorf("%s: cannot_tell %+v", g, s.CannotTell)
		}
	}

	for _, g := range []string{"shop/money", "shop/orders", "shop/via"} {
		if len(byGroup[g].CannotTell) > 0 {
			t.Errorf("%s builds, yet can't tell %+v", g, byGroup[g].CannotTell)
		}
	}

	for _, paths := range [][]string{{"shop/via/via.go"}, {"shop/typed/typed.go"}, {"shop/gated/flow_test.go"}, {"shop/syntax/syntax.go", "shop/stale/stale.go"}} {
		got, _ := asked(t, root, paths)
		for _, s := range got {
			if w := byGroup[s.Group]; !reflectEqual(t, s, w) {
				t.Errorf("%v: set %s differs from the whole tree's:\n got %+v\nwant %+v", paths, s.Group, s, w)
			}
		}
	}
}

func reflectEqual(t *testing.T, a, b plugin.Set) bool {
	t.Helper()

	x, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}

	y, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}

	return string(x) == string(y)
}

func TestLinksAnswerTheReadersOfAConstructor(t *testing.T) {
	root := fixture(t)
	writeFile(t, root, "shop/problem/problem.go", "package problem\n\ntype Problem struct{ Code string }\n\nfunc (p *Problem) Error() string { return p.Code }\n\n"+
		"// New is a problem.\nfunc New(code string) error { return &Problem{Code: code} }\n")
	writeFile(t, root, "shop/money/errors.go", "package money\n\nimport \"example.com/shop/problem\"\n\n// ErrOver is over.\nvar ErrOver = problem.New(\"over\")\n")
	// a dot import, which the syntax can't follow: a reader of every file
	// until its set says otherwise, and then not answered
	writeFile(t, root, "shop/dotty/dotty.go", "package dotty\n\nimport . \"strings\"\n\n// Low is low.\nvar Low = ToLower(\"A\")\n")

	whole, err := graph.Load(t.Context(), root, graph.Options{})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]plugin.Set{}
	for _, s := range whole.LinkSets(nil) {
		want[s.Group] = setOf(s)
	}

	if !slices.Contains(want["shop/money"].Inputs, "shop/problem/problem.go") {
		t.Fatalf("money's inputs %v lack the constructor's file", want["shop/money"].Inputs)
	}

	sets, _ := asked(t, root, []string{"shop/problem/problem.go"})
	if got := groups(sets); !slices.Equal(got, []string{"shop/money", "shop/problem"}) {
		t.Errorf("sets of %v, want money's and problem's", got)
	}

	for _, s := range sets {
		if !equalSets(s, want[s.Group]) {
			t.Errorf("%s answers %+v, the whole load %+v", s.Group, s, want[s.Group])
		}
	}
}

func writeFile(t *testing.T, root, rel, src string) {
	t.Helper()

	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
}
