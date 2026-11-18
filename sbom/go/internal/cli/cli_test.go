package cli

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"regexp"
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

func tree(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	write(t, root, map[string]string{
		"shop/go.mod":                  "module example.com/shop\n\ngo 1.25\n\nrequire example.com/x v1.0.0\n",
		"shop/go.sum":                  "example.com/x v1.0.0 h1:X=\n",
		"shop/money/money.go":          "package money\n\nconst Rate = 19\n\nfunc Gross(c int) int { return c * (100 + Rate) / 100 }\n",
		"shop/money/money_test.go":     "package money\n\nimport \"testing\"\n\nfunc TestGross(t *testing.T) { _ = Gross(1) }\n",
		"shop/pages/pages.go":          "package pages\n\nimport _ \"embed\"\n\n//go:embed static/index.html\nvar index string\n",
		"shop/pages/static/index.html": "<p>hi</p>\n",
		"shop/money/testdata/x.go":     "package broken(\n",
		"shop/vendor/v/v.go":           "package v\n",
		"tools/go.mod":                 "module example.com/tools\n",
		"tools/tool.go":                "package tools\n\nfunc Run() {}\n",
	})

	return root
}

func run(t *testing.T, name string, req map[string]any) (int, string, string) {
	t.Helper()

	in, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer

	code := Run([]string{name}, bytes.NewReader(in), &out, &errOut, "v1.2.3")

	return code, out.String(), errOut.String()
}

func request(t *testing.T, root string, extra map[string]any) map[string]any {
	t.Helper()

	req := map[string]any{
		"protocol": 1, "work": t.TempDir(), "cache": t.TempDir(), "tree": root,
		"config": map[string]any{"gomodcache": t.TempDir()},
	}
	maps.Copy(req, extra)

	return req
}

func unitsOf(t *testing.T, root string, extra map[string]any) UnitsAnswer {
	t.Helper()

	code, out, errOut := run(t, "units", request(t, root, extra))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}

	var a UnitsAnswer
	if err := json.Unmarshal([]byte(out), &a); err != nil {
		t.Fatal(err)
	}

	return a
}

func groups(a UnitsAnswer) []string {
	out := make([]string, 0, len(a.Sets))
	for _, s := range a.Sets {
		out = append(out, s.Group)
	}

	return out
}

func set(t *testing.T, a UnitsAnswer, group string) Set {
	t.Helper()

	for _, s := range a.Sets {
		if s.Group == group {
			return s
		}
	}

	t.Fatalf("no set %s among %v", group, groups(a))

	return Set{}
}

func TestDescribe(t *testing.T) {
	code, out, _ := run(t, "describe", nil)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}

	for _, want := range []string{`"kind":"sbom"`, `"version":"v1.2.3"`, `"units":"per-set"`, `"detect":"per-file"`, `"**/go.mod"`, `"name":"module-graph","off_at":5`, `"namespaces":["go","dep:go"]`, `"fill":{"gomodcache":{"files":["go.mod","go.sum"],"run":["go","mod","download"]}}`} {
		if !strings.Contains(out, want) {
			t.Errorf("describe %s lacks %s", out, want)
		}
	}
}

func TestUnitsOfTheWholeTree(t *testing.T) {
	root := tree(t)
	a := unitsOf(t, root, nil)

	if got := groups(a); !slices.Equal(got, []string{"shop/money", "shop/pages", "tools", "dep:go"}) {
		t.Errorf("sets %v: want each package once, testdata and vendor skipped, the dependencies last", got)
	}

	money := set(t, a, "shop/money")
	if !slices.Equal(money.Inputs, []string{"shop/money/money.go", "shop/money/money_test.go"}) {
		t.Errorf("inputs %v", money.Inputs)
	}

	ids := map[string]Unit{}
	for _, u := range money.Units {
		ids[u.ID] = u
	}

	if g := ids["go:shop/money#func:Gross"]; !g.Executable || g.Kind != "func" || g.Group != "shop/money" || g.Digest == "" {
		t.Errorf("Gross: %+v", g)
	}

	if _, ok := ids["go:shop/money#func:TestGross@test"]; !ok {
		t.Errorf("no unit for the test function: %v", slices.Sorted(maps.Keys(ids)))
	}

	if pages := set(t, a, "shop/pages"); !slices.Contains(pages.Inputs, "shop/pages/static/index.html") {
		t.Errorf("pages inputs %v lack the embedded file", pages.Inputs)
	}

	deps := set(t, a, "dep:go")
	if len(deps.Units) == 0 || deps.Units[0].ID != "dep:go:example.com/x" || deps.Units[0].Kind != "module" ||
		deps.Units[0].Attributes["version"] != "v1.0.0" || deps.Units[0].Attributes["h1"] != "h1:X=" {
		t.Errorf("deps %+v", deps.Units)
	}
}

func TestUnitsForPaths(t *testing.T) {
	root := tree(t)

	cases := map[string]struct {
		paths []string
		want  []string
	}{
		"a Go file answers its package":                  {paths: []string{"shop/money/money.go"}, want: []string{"shop/money"}},
		"an embedded file answers the packages above it": {paths: []string{"shop/pages/static/index.html"}, want: []string{"shop/pages"}},
		"a file below testdata answers nothing":          {paths: []string{"shop/money/testdata/x.go"}, want: []string{}},
		"go.sum answers the dependencies":                {paths: []string{"shop/go.sum"}, want: []string{"dep:go"}},
		"a module not configured answers nothing": {
			paths: []string{"tools/tool.go"}, want: []string{},
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			extra := map[string]any{"paths": c.paths}
			if strings.Contains(name, "not configured") {
				extra["config"] = map[string]any{"modules": []string{"shop"}}
			}

			if got := groups(unitsOf(t, root, extra)); !slices.Equal(got, c.want) {
				t.Errorf("sets %v, want %v", got, c.want)
			}
		})
	}
}

func TestUnitsOfAGonePackageAreEmpty(t *testing.T) {
	root := tree(t)
	if err := os.RemoveAll(filepath.Join(root, "shop/money")); err != nil {
		t.Fatal(err)
	}

	s := set(t, unitsOf(t, root, map[string]any{"paths": []string{"shop/money/money.go"}}), "shop/money")
	if len(s.Units) != 0 || len(s.Inputs) != 0 {
		t.Errorf("gone package: %+v", s)
	}
}

func TestModuleGraphLinksAreTaggedAndAlwaysThere(t *testing.T) {
	root := tree(t)
	cache := t.TempDir()
	write(t, root, map[string]string{
		"shop/go.mod": "module example.com/shop\n\nrequire (\n\texample.com/x v1.0.0\n\texample.com/y v1.0.0\n\texample.com/z v1.0.0\n)\n",
	})
	write(t, cache, map[string]string{"cache/download/example.com/x/@v/v1.0.0.mod": "module example.com/x\n\nrequire example.com/y v1.0.0\n"})

	cfg := map[string]any{"gomodcache": cache, "modules": []string{"shop"}}

	a := unitsOf(t, root, map[string]any{"config": cfg, "paths": []string{"shop/go.mod"}, "narrowings": []string{NarrowModuleGraph}})
	s := set(t, a, "dep:go")

	if len(s.Links) != 1 || s.Links[0].Narrowing != NarrowModuleGraph || s.Links[0].While != "off" || s.Links[0].To != "dep:go:example.com/y" {
		t.Errorf("links %+v: want x requires y, standing while module-graph is off", s.Links)
	}

	if len(s.CannotTell) != 2 {
		t.Errorf("cannot tell %+v: want y and z, whose go.mod isn't cached", s.CannotTell)
	}

	if a.Cacheable == nil || *a.Cacheable {
		t.Error("an answer that depends on the module cache is marked cacheable")
	}

	write(t, cache, map[string]string{
		"cache/download/example.com/y/@v/v1.0.0.mod": "module example.com/y\n",
		"cache/download/example.com/z/@v/v1.0.0.mod": "module example.com/z\n",
	})

	if a := unitsOf(t, root, map[string]any{"config": cfg, "paths": []string{"shop/go.mod"}}); a.Cacheable != nil {
		t.Error("a complete answer is marked uncacheable")
	}
}

func TestDocument(t *testing.T) {
	code, out, errOut := run(t, "document", request(t, tree(t), nil))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}

	var doc bom
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}

	if doc.BOMFormat != "CycloneDX" || doc.SpecVersion != "1.6" || !strings.HasPrefix(doc.SerialNumber, "urn:uuid:") {
		t.Errorf("header %+v", doc)
	}

	found := false

	for _, c := range doc.Components {
		if c.PURL == "pkg:golang/example.com/x@v1.0.0" && slices.Contains(c.Properties, property{Name: "go:h1", Value: "h1:X="}) {
			found = true
		}
	}

	if !found {
		t.Errorf("components %+v lack example.com/x with its checksum", doc.Components)
	}

	// x's go.mod isn't in the cache: its dependencies are unknown, so it
	// has no entry, while the main module's lists x
	for _, d := range doc.Dependencies {
		if d.Ref == "pkg:golang/example.com/x@v1.0.0" {
			t.Error("a module whose requirements are unknown has a dependencies entry")
		}
	}

	_, again, _ := run(t, "document", request(t, tree(t), nil))
	if again != out {
		t.Error("the document isn't the same for the same files")
	}
}

func TestRequestErrors(t *testing.T) {
	if code, _, _ := run(t, "units", map[string]any{"protocol": 2, "tree": "."}); code != exitFailed {
		t.Errorf("protocol 2: exit %d", code)
	}

	if code, _, _ := run(t, "nope", map[string]any{"protocol": 1, "tree": "."}); code != exitFailed {
		t.Errorf("unknown call: exit %d", code)
	}
}

func TestDetect(t *testing.T) {
	root := tree(t)
	write(t, root, map[string]string{"shop/gen/api.gen.go": "// Code generated by some-tool. DO NOT EDIT.\n\npackage gen\n"})

	code, out, errOut := run(t, "detect", request(t, root, map[string]any{"paths": []string{
		"shop/money/money.go", "shop/gen/api.gen.go", "shop/go.mod", "shop/go.sum", "shop/money/testdata/x.go", "shop/gone.go",
	}}))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}

	want := `{"claims":[{"path":"shop/money/money.go","role":"source"},{"path":"shop/gen/api.gen.go","role":"generated"},` +
		`{"path":"shop/go.mod","role":"manifest"},{"path":"shop/go.sum","role":"lockfile"}]}`
	if strings.TrimSpace(out) != want {
		t.Errorf("detect = %s\nwant     %s", out, want)
	}
}

func TestUnitsOfClaimedFilesOnly(t *testing.T) {
	root := tree(t)

	a := unitsOf(t, root, map[string]any{"claimed": []map[string]string{
		{"path": "shop/go.mod", "role": "manifest"}, {"path": "shop/money/money.go", "role": "source"},
	}})

	if got := groups(a); !slices.Equal(got, []string{"shop/money", "dep:go"}) {
		t.Errorf("sets %v: want the claimed package and the claimed module's dependencies", got)
	}
}

func TestUnitsOfASymlinkedTree(t *testing.T) {
	root := tree(t)

	link := filepath.Join(t.TempDir(), "source")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}

	want, err := json.Marshal(unitsOf(t, root, nil))
	if err != nil {
		t.Fatal(err)
	}

	got, err := json.Marshal(unitsOf(t, link, nil))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, want) {
		t.Errorf("units through the link differ:\n%s\nwant\n%s", got, want)
	}

	code, doc, errOut := run(t, "document", request(t, link, nil))
	if code != 0 || !strings.Contains(doc, "example.com/x") {
		t.Errorf("document through the link: exit %d %s %s", code, doc, errOut)
	}
}

func TestUnitsOfATreeWithoutModulesCannotTell(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"a/a.go": "package a\n"})

	for _, name := range []string{"units", "document"} {
		code, out, errOut := run(t, name, request(t, root, nil))
		if code != exitCannotTell || !strings.Contains(errOut, "no go.mod") {
			t.Errorf("%s: exit %d %q %q, want %d naming the missing go.mod", name, code, out, errOut, exitCannotTell)
		}
	}

	a := unitsOf(t, root, map[string]any{"claimed": []map[string]string{}})
	if len(a.Sets) != 1 || a.Sets[0].Group != "dep:go" || len(a.Sets[0].Units) != 0 {
		t.Errorf("with nothing claimed: %+v", a)
	}
}

func TestDetectOfAMissingTreeFails(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "gone")

	code, out, _ := run(t, "detect", request(t, gone, map[string]any{"paths": []string{"go.mod"}}))
	if code != exitFailed {
		t.Errorf("exit %d %s, want %d", code, out, exitFailed)
	}
}

func TestUnitsOfAPackageThatDoesNotParse(t *testing.T) {
	root := tree(t)
	before := set(t, unitsOf(t, root, nil), "shop/pages")

	write(t, root, map[string]string{"shop/money/broken.go": "package money\n\nfunc Broken( {\n"})

	a := unitsOf(t, root, nil)
	money := set(t, a, "shop/money")

	var ids []string
	for _, u := range money.Units {
		ids = append(ids, u.ID)
	}

	if want := []string{"go:shop/money#file:broken.go", "go:shop/money#file:money.go", "go:shop/money#file:money_test.go"}; !slices.Equal(ids, want) || !money.Whole {
		t.Errorf("units %q (whole %v), want %q", ids, money.Whole, want)
	}

	if len(money.CannotTell) != len(ids) {
		t.Fatalf("cannot_tell %+v, want one per unit", money.CannotTell)
	}

	want := "package shop/money does not build with tags [pipelinegen_cover]: broken.go:3:"
	if r := money.CannotTell[0].Reason; !strings.HasPrefix(r, want) || !strings.HasSuffix(r, "tests that reach it run on every change until it builds or is excluded") {
		t.Errorf("reason %q", r)
	}

	if after := set(t, a, "shop/pages"); !slices.EqualFunc(after.Units, before.Units, func(x, y Unit) bool { return x.ID == y.ID && x.Digest == y.Digest }) {
		t.Error("another package's units changed")
	}
}

func TestUnitsCarryLabels(t *testing.T) {
	root := tree(t)
	a := unitsOf(t, root, nil)

	for _, s := range a.Sets {
		for _, u := range s.Units {
			if u.Label == "" || len([]rune(u.Label)) > 80 || strings.Contains(u.Label, root) {
				t.Errorf("%s: label %q", u.ID, u.Label)
			}
		}
	}

	got := map[string]string{}
	for _, u := range set(t, a, "shop/money").Units {
		got[u.ID] = u.Label
	}

	for id, want := range map[string]string{
		"go:shop/money#func:Gross":          "money.Gross",
		"go:shop/money#const:Rate":          "money.Rate",
		"go:shop/money#func:TestGross@test": "money.TestGross (test)",
	} {
		if got[id] != want {
			t.Errorf("%s: label %q, want %q", id, got[id], want)
		}
	}

	defer func(f func(goid.ID, string) string) { labelOf = f }(labelOf)

	labelOf = func(goid.ID, string) string { return "x" }
	b := unitsOf(t, root, nil)

	strip := func(sets []Set) string {
		out, err := json.Marshal(sets)
		if err != nil {
			t.Fatal(err)
		}

		return regexp.MustCompile(`"label":"[^"]*",?`).ReplaceAllString(string(out), "")
	}

	if strip(a.Sets) != strip(b.Sets) {
		t.Error("an answer changes with its labels")
	}
}
