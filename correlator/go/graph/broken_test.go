package graph

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// brokenFiles add to the shop one package of each kind that doesn't build,
// and two packages that import them:
//   - gated: every file behind a build tag the build doesn't set;
//   - stale: the package builds, a test helper no longer fits an interface;
//   - typed: the package itself doesn't type-check;
//   - syntax: a file doesn't parse;
//   - via imports typed and calls one of its functions;
//   - user imports stale, whose own code builds.
var brokenFiles = map[string]string{
	"shop/gated/flow_test.go": "//go:build e2e\n\npackage gated\n\nimport (\n\t\"testing\"\n\n\t\"example.com/shop/orders\"\n)\n\nfunc TestFlow(t *testing.T) { _ = orders.Create(1) }\n",
	"shop/stale/stale.go":     "package stale\n\nimport \"example.com/shop/money\"\n\n// Use makes one amount.\nfunc Use() money.Amount { return money.Gross(money.Amount{Cents: 1}) }\n",
	"shop/stale/helpers_test.go": "package stale\n\nimport \"testing\"\n\ntype finder interface{ Find(id int) int }\n\ntype stub struct{}\n\nfunc (stub) Find(id string) int { return len(id) }\n\n" +
		"var _ finder = stub{}\n\nfunc TestUse(t *testing.T) { _ = Use() }\n",
	"shop/typed/typed.go":    "package typed\n\nimport \"example.com/shop/money\"\n\n// Bad no longer type-checks.\nfunc Bad() money.Amount { return money.Gross(1) }\n\n// Good does.\nfunc Good() int { return money.Rate }\n",
	"shop/syntax/syntax.go":  "package syntax\n\nfunc Broken( {\n",
	"shop/syntax/ok.go":      "package syntax\n\n// OK parses.\nfunc OK() int { return 1 }\n",
	"shop/via/via.go":        "package via\n\nimport \"example.com/shop/typed\"\n\n// Via calls into typed.\nfunc Via() int { return typed.Good() }\n",
	"shop/via/via_test.go":   "package via\n\nimport \"testing\"\n\nfunc TestVia(t *testing.T) { _ = Via() }\n",
	"shop/user/user.go":      "package user\n\nimport \"example.com/shop/stale\"\n\n// U uses stale's own code.\nfunc U() int { return stale.Use().Cents }\n",
	"shop/user/user_test.go": "package user\n\nimport \"testing\"\n\nfunc TestU(t *testing.T) { _ = U() }\n",
}

func brokenTree(t *testing.T) (clean, broken *Tree) {
	t.Helper()

	root := fixture(t)
	clean = load(t, root)

	root = fixture(t)
	for name, src := range brokenFiles {
		write(t, root, name, src)
	}

	return clean, load(t, root)
}

func TestBrokenPackagesAreNamed(t *testing.T) {
	_, tr := brokenTree(t)

	got := tr.Broken()
	if dirs := sortedKeys(got); !slices.Equal(dirs, []string{"shop/gated", "shop/stale", "shop/syntax", "shop/typed"}) {
		t.Fatalf("broken %q, want gated, stale, syntax and typed", dirs)
	}

	for dir, want := range map[string]string{
		"shop/gated":  "build constraints exclude all Go files in shop/gated",
		"shop/stale":  "shop/stale/helpers_test.go:11:16: cannot use stub{}",
		"shop/syntax": "shop/syntax/syntax.go:3:",
		"shop/typed":  "shop/typed/typed.go:6:46: cannot use 1",
	} {
		prefix := "package " + dir + " does not build with tags [pipelinegen_cover]: "
		if r := got[dir]; !strings.HasPrefix(r, prefix+want) || !strings.HasSuffix(r, "; tests that reach it run on every change until it builds or is excluded") {
			t.Errorf("%s: %q, want %q", dir, r, prefix+want+"...")
		}

		if strings.Contains(got[dir], tr.Root) {
			t.Errorf("%s: the reason names the checkout: %q", dir, got[dir])
		}
	}

	if p := tr.Package("shop/stale"); !p.BrokenTests || p.Whole != "" {
		t.Errorf("stale: only its tests fail, yet %+v", p)
	}

	if p := tr.Package("shop/typed"); p.BrokenTests || p.Whole == "" {
		t.Errorf("typed fails as a package, yet %+v", p)
	}
}

func TestBrokenPackagesLeaveOtherLinksExact(t *testing.T) {
	clean, tr := brokenTree(t)

	want := map[string]LinkSet{}
	for _, s := range clean.LinkSets(nil) {
		want[s.Group] = s
	}

	sets := map[string]LinkSet{}
	for _, s := range tr.LinkSets(nil) {
		sets[s.Group] = s
	}

	for group, w := range want {
		if !reflect.DeepEqual(sets[group], w) {
			t.Errorf("%s's links changed:\n got %+v\nwant %+v", group, sets[group], w)
		}
	}

	for _, group := range []string{"shop/via", "shop/user"} {
		if len(sets[group].CannotTell) > 0 {
			t.Errorf("%s builds, yet can't tell %+v", group, sets[group].CannotTell)
		}
	}

	// an importer's reference into a broken package names its unit, which
	// can't be told: what reaches it is bound wide
	if !slices.ContainsFunc(sets["shop/via"].Links, func(l Link) bool {
		return l.From == "go:shop/via#func:Via" && l.To == "go:shop/typed#func:Good"
	}) {
		t.Error("via's reference into typed is gone")
	}

	cannot := func(group string) []string {
		var out []string
		for _, c := range sets[group].CannotTell {
			out = append(out, c.Unit)

			if c.Reason != tr.Package(group).Broken {
				t.Errorf("%s: reason %q", group, c.Reason)
			}
		}

		return out
	}

	for group, units := range map[string][]string{
		"shop/gated":  {"go:shop/gated#file:flow_test.go"},
		"shop/stale":  {"go:shop/stale#func:TestUse@test", "go:shop/stale#method:stub.Find@test", "go:shop/stale#type:finder@test", "go:shop/stale#type:stub@test", "go:shop/stale#typedecl:stub@test", "go:shop/stale#var:_:helpers_test.go:1@test"},
		"shop/syntax": {"go:shop/syntax#file:ok.go", "go:shop/syntax#file:syntax.go"},
		"shop/typed":  {"go:shop/typed#func:Bad", "go:shop/typed#func:Good"},
	} {
		if got := cannot(group); !slices.Equal(slices.Sorted(slices.Values(got)), units) {
			t.Errorf("%s can't tell %q, want %q", group, got, units)
		}
	}
}

func TestBrokenPackagesBindWideOnTheirOwn(t *testing.T) {
	clean, tr := brokenTree(t)
	req := Request{Slices: shopSlices(), Narrowings: narrowed}

	if got, want := tr.Bind(req).Results, clean.Bind(req).Results; !reflect.DeepEqual(got, want) {
		t.Errorf("the shop's slices bind otherwise:\n got %+v\nwant %+v", got, want)
	}

	everything := func(s Slice) []bool {
		var out []bool
		for _, r := range tr.Bind(Request{Slices: []Slice{s}, Narrowings: narrowed}).Results {
			out = append(out, r.Binding.Everything)
		}

		return out
	}

	for name, c := range map[string]struct {
		s    Slice
		wide bool
	}{
		"a slice whose files are all behind a tag": {Slice{Suite: "e2e", Name: "shop/gated/flow_test.go", HasList: true, Sources: []string{"shop/gated/flow_test.go"}}, true},
		"a slice of tests that don't type-check":   {Slice{Suite: "go", Name: "shop/stale/helpers_test.go", HasList: true, Sources: []string{"shop/stale/helpers_test.go"}}, true},
		"an importer that calls into the package": {Slice{Suite: "go", Name: "shop/via/via_test.go", HasList: true, Sources: []string{"shop/via/via_test.go"},
			List: []string{"go:shop/via#func:Via", "go:shop/typed#func:Good"}}, true},
		"a slice that links a package whose tests alone fail": {Slice{Suite: "go", Name: "shop/user/user_test.go", HasList: true, Sources: []string{"shop/user/user_test.go"},
			List: []string{"go:shop/user#func:U", "go:shop/stale#func:Use", "go:shop/money#func:Gross"}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := everything(c.s); !slices.Equal(got, []bool{c.wide, c.wide}) {
				t.Errorf("everything %v in the two widths, want %v", got, c.wide)
			}
		})
	}
}

func TestBrokenPackagesTheConfigExcludes(t *testing.T) {
	root := fixture(t)
	for name, src := range brokenFiles {
		write(t, root, name, src)
	}

	claimed := map[string]bool{}

	for _, f := range []string{"shop/money/money.go", "shop/orders/orders.go", "shop/orders/create_test.go", "shop/orders/cancel_test.go",
		"shop/cmd/shop/main.go", "shop/stale/stale.go", "shop/stale/helpers_test.go", "shop/typed/typed.go",
		"shop/via/via.go", "shop/via/via_test.go", "shop/user/user.go", "shop/user/user_test.go"} {
		claimed[f] = true
	}

	tr, err := Load(t.Context(), root, Options{Claimed: claimed})
	if err != nil {
		t.Fatal(err)
	}

	if dirs := sortedKeys(tr.Broken()); !slices.Equal(dirs, []string{"shop/stale", "shop/typed"}) {
		t.Errorf("broken %q, want stale and typed: gated and syntax are excluded", dirs)
	}

	for _, s := range tr.LinkSets(nil) {
		if s.Group == "shop/gated" || s.Group == "shop/syntax" {
			t.Errorf("an excluded package answers a set: %+v", s)
		}
	}

	r := tr.Bind(Request{Slices: []Slice{{Suite: "e2e", Name: "shop/gated/flow_test.go", HasList: true, Sources: []string{"shop/gated/flow_test.go"}}}, Narrowings: narrowed})
	for _, res := range r.Results {
		if res.Binding.Everything {
			t.Errorf("an excluded package binds wide: %+v", res)
		}
	}
}
