package graph

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen-sbom-go/goid"
)

// lab is a module with what the shop fixture lacks: interfaces and their
// implementations, inits and initialisers that act, a package bound whole,
// test types that escape to an interface and ones that don't, TestMain, a
// test variable with an initialiser, and an external test package.
var lab = map[string]string{
	"lab/core/core.go": `package core

import "errors"

type Store interface{ Get() int }

type Mem struct{ n int }

func (m Mem) Get() int { return m.n + Max }

func (Mem) Noop() {}

func (m *Mem) Set(n int) { m.n = n }

const Max = 3

const Limit = 9

var registry = map[string]int{"a": seed()}

var ErrNone = errors.New("none")

var plain = Max

func seed() int { return 2 }

func init() { registry["b"] = 1 }

func Use(s Store) int { return s.Get() + plain }

func Empty() {}

func Size() int { return len(registry) }

type Pair struct{ A int }

type Twin Pair

type Same = Pair

func MakeTwin() Twin { return Twin{A: 1} }
`,
	"lab/core/twice.go": `package core

import "example.com/ext"

func Double(n int) int { return ext.Twice(n) }
`,
	"lab/whole/whole.go": `package whole

import "reflect"

func Call(v any) { reflect.ValueOf(v).MethodByName("Run") }
`,
	"lab/extra/extra.go": `package extra

var Ready = start()

func start() bool { println("up"); return true }

func init() { Ready = !Ready }
`,
	"lab/svc/svc.go": `package svc

import (
	"example.com/lab/core"
	"example.com/lab/whole"
)

type Service struct{ S core.Store }

func (s Service) Run() int { whole.Call(s); return core.Use(s.S) }

func (Service) Name() string { return "svc" }

func New() Service { core.Empty(); return Service{S: core.Mem{}} }

func Sum() int { return core.MakeTwin().A }
`,
	"lab/svc/svc_test.go": `package svc

import (
	"os"
	"testing"

	"example.com/lab/core"
	"example.com/lab/extra"
)

var _ = extra.Ready

var fixture = core.Size()

type env struct{ n int }

func (e env) Get() int { return e.n }

func (env) helper() int { return 1 }

type loud struct{ m core.Mem }

func TestLoud(t *testing.T) {
	var s core.Store = loud{}
	_ = s
}

func TestMain(m *testing.M) { os.Exit(m.Run()) }

func init() { _ = fixture }

func TestRun(t *testing.T) {
	s := Service{S: env{n: 1}}
	if s.Run() == 0 {
		t.Fatal()
	}
}

func TestName(t *testing.T) {
	var q quiet
	_ = q.Get()
	if New().Name() == "" {
		t.Fatal()
	}
}
`,
	"lab/svc/helpers_test.go": `package svc

import (
	"example.com/lab/core"
	"example.com/other"
)

var one = other.One

type quiet struct{}

func (quiet) Get() int { return 0 }

func (quiet) unused() {}

func (quiet) other() int { return core.Limit }

func (l loud) Get() int { return l.m.Get() }
`,
	"lab/svc/ext_test.go": `package svc_test

import (
	"testing"

	"example.com/lab/svc"
	"example.com/lab/svc/svctest"
)

func TestExt(t *testing.T) { _ = svc.New(); _ = svctest.Fake() }
`,
	"lab/svc/svctest/svctest.go": `package svctest

import "example.com/lab/svc"

func Fake() svc.Service { return svc.New() }
`,
}

func labTree(t *testing.T) *Tree {
	t.Helper()

	return load(t, labRoot(t))
}

func labRoot(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	for rel, src := range lab {
		write(t, root, rel, src)
	}

	// modules from outside the tree, vendored, one importing the other and
	// one imported only by tests: what a package links of other modules
	write(t, root, "lab/go.mod", "module example.com/lab\n\ngo 1.24\n\nrequire (\n\texample.com/ext v1.0.0\n\texample.com/other v1.0.0\n)\n\n"+
		"require example.com/deep v1.0.0 // indirect\n")
	write(t, root, "lab/vendor/modules.txt", "# example.com/deep v1.0.0\n## explicit; go 1.24\nexample.com/deep\n"+
		"# example.com/ext v1.0.0\n## explicit; go 1.24\nexample.com/ext\n# example.com/other v1.0.0\n## explicit; go 1.24\nexample.com/other\n")
	write(t, root, "lab/vendor/example.com/ext/ext.go", "package ext\n\nimport \"example.com/deep\"\n\nfunc Twice(n int) int { return deep.Add(n, n) }\n")
	write(t, root, "lab/vendor/example.com/deep/deep.go", "package deep\n\nfunc Add(a, b int) int { return a + b }\n")
	write(t, root, "lab/vendor/example.com/other/other.go", "package other\n\nfunc One() int { return 1 }\n")

	return root
}

func allSets(t *testing.T, tree *Tree) []LinkSet {
	t.Helper()

	n, err := ReadNames(t.Context(), tree.Root, NamesOptions{})
	if err != nil {
		t.Fatal(err)
	}

	tree.SetNames(n)

	return append(tree.LinkSets(nil), n.Set())
}

func holds(l Link, on map[string]bool) bool {
	switch {
	case l.Narrowing == "":
		return true
	case l.While == WhileOn:
		return on[l.Narrowing]
	default:
		return !on[l.Narrowing]
	}
}

func closure(sets []LinkSet, roots []string, on map[string]bool) [2]map[string]bool {
	from := map[string][]Link{}

	for _, s := range sets {
		for _, l := range s.Links {
			if holds(l, on) {
				from[l.From] = append(from[l.From], l)
			}
		}
	}

	walk := func(bound map[string]bool, start []string, all bool) {
		expanded := map[string]bool{}
		queue := slices.Clone(start)

		for _, r := range start {
			bound[r] = true
		}

		for len(queue) > 0 {
			n := queue[0]
			queue = queue[1:]

			if expanded[n] {
				continue
			}

			expanded[n] = true

			for _, l := range from[n] {
				switch {
				case all || l.Kind == KindFollow:
					bound[l.To] = true
					queue = append(queue, l.To)
				case l.Kind == KindBind:
					bound[l.To] = true
				}
			}
		}
	}

	var out [2]map[string]bool

	out[Executed] = map[string]bool{}
	walk(out[Executed], roots, false)

	out[Dependents] = map[string]bool{}
	walk(out[Dependents], slices.Collect(maps.Keys(out[Executed])), true)

	return out
}

func (t *Tree) roots(s Slice) []string {
	out := slices.Clone(s.List)

	for _, f := range s.Sources {
		for _, i := range t.testFiles[f] {
			d := t.Decls[i]
			if len(s.Tests) > 0 && isTestFunc(d) && !slices.Contains(s.Tests, testName(d)) {
				continue
			}

			if t.hasTwin(i) {
				out = append(out, t.twin(i))

				continue
			}

			out = append(out, string(d.ID))
		}
	}

	return out
}

func (t *Tree) unitsOf(b binding) map[string]bool {
	bd := t.binding(b, nil)
	out := map[string]bool{}

	for id := range bd.Units {
		out[id] = true
	}

	for _, dir := range bd.Whole {
		p := t.pkgs[dir]

		for _, i := range slices.Concat(p.Decls, p.TestDecls) {
			out[t.unitID(b, i)] = true

			if t.hasDecl(i) {
				out[string(t.Decls[i].ID)] = true
			}

			for _, m := range t.methods[i] {
				if sig, ok := t.sigID(m); ok {
					out[sig] = true
				}
			}
		}

		// its API unit is an input of the sets that import it, which
		// the planner doesn't bind with the package whole
		for _, id := range p.Excluded {
			out[string(id)] = true
		}
	}

	return out
}

func units(bound map[string]bool) map[string]bool {
	out := map[string]bool{}

	for id := range bound {
		if strings.HasPrefix(id, "group:") || strings.HasPrefix(id, "node:") {
			continue
		}

		out[id] = true
	}

	for id := range out {
		if dir, kind, name, err := goid.Parse(id); err == nil && kind == goid.TypeDecl && out[string(goid.New(dir, goid.Type, name))] {
			delete(out, id)
		}
	}

	return out
}

func diff(a, b map[string]bool) []string {
	var out []string

	for id := range a {
		if !b[id] {
			out = append(out, id)
		}
	}

	slices.Sort(out)

	return out
}

func levels() []map[string]bool {
	all := []string{NarrowMethodSets, NarrowTestMethodSets, NarrowPureInitialisers, NarrowPureFunctions, NarrowPureTestVariables, NarrowModuleBumps, NarrowInterfaceMethods}

	out := []map[string]bool{{}}

	on := map[string]bool{}
	for _, n := range all {
		on[n] = true
	}

	out = append(out, on)

	for _, n := range all {
		one := maps.Clone(on)
		delete(one, n)
		out = append(out, one)
	}

	return out
}

func TestLinksEqualBind(t *testing.T) {
	shop := load(t, fixture(t))
	labT := labTree(t)

	cases := []struct {
		tree   *Tree
		slices []Slice
	}{
		{shop, shopSlices()},
		{labT, []Slice{
			{Name: "run", HasList: true, Sources: []string{"lab/svc/svc_test.go"}, Tests: []string{"TestRun"},
				List: []string{"go:lab/svc#method:Service.Run", "go:lab/core#func:Use"}},
			{Name: "name", HasList: true, Sources: []string{"lab/svc/svc_test.go"}, Tests: []string{"TestName"},
				List: []string{"go:lab/svc#func:New", "go:lab/svc#method:Service.Name"}},
			{Name: "ext", HasList: true, Sources: []string{"lab/svc/ext_test.go"}, List: []string{"go:lab/svc#func:New"}},
			{Name: "core", HasList: true, List: []string{"go:lab/core#method:Mem.Get"}},
		}},
	}

	for _, c := range cases {
		sets := allSets(t, c.tree)

		for _, on := range levels() {
			o := optionsOf(on)

			for _, s := range c.slices {
				want := c.tree.bind(s, o)
				got := closure(sets, c.tree.roots(s), on)

				exec, wantExec := units(got[Executed]), c.tree.unitsOf(want[Executed])
				if missing, extra := diff(wantExec, exec), diff(exec, wantExec); len(missing)+len(extra) > 0 {
					t.Errorf("%s under %v, executed: links miss %v and add %v", s.Name, slices.Sorted(maps.Keys(on)), missing, extra)
				}

				dep, wantDep := units(got[Dependents]), c.tree.unitsOf(want[Dependents])
				if missing := diff(wantDep, dep); len(missing) > 0 {
					t.Errorf("%s under %v, dependents: links miss %v", s.Name, slices.Sorted(maps.Keys(on)), missing)
				}
			}
		}
	}
}

func TestLinkEndsAreListed(t *testing.T) {
	for _, root := range []string{fixture(t), labRoot(t), replacedRoot(t)} {
		tree := load(t, root)
		inv := listUnits(t, root)

		for _, s := range allSets(t, tree) {
			for _, l := range s.Links {
				for _, end := range []string{l.From, l.To} {
					switch {
					case strings.HasPrefix(end, "node:"):
					case strings.HasPrefix(end, "group:"):
						if len(inv.groups[strings.TrimPrefix(end, "group:")]) == 0 {
							t.Errorf("%s: link %s -> %s names a group no unit is in", s.Group, l.From, l.To)
						}
					default:
						if _, ok := inv.digests[end]; !ok {
							t.Errorf("%s: link %s -> %s names %s, which go-sbom doesn't list", s.Group, l.From, l.To, end)
						}
					}
				}
			}
		}

		twins, sigs := map[string]bool{}, map[string]bool{}

		for i := range tree.Decls {
			if tree.hasTwin(int32(i)) || tree.hasDecl(int32(i)) {
				twins[tree.twin(int32(i))] = true
			}

			if sig, ok := tree.sigID(int32(i)); ok {
				sigs[sig] = true
			}
		}

		for id := range inv.digests {
			switch _, kind, _, err := goid.Parse(id); {
			case err != nil:
			case kind == goid.TypeDecl && !twins[id]:
				t.Errorf("go-sbom lists %s, the links know no such declaration", id)
			case kind == goid.MethodSig && !sigs[id]:
				t.Errorf("go-sbom lists %s, the links know no such signature", id)
			}
		}

		for id := range twins {
			if _, ok := inv.digests[id]; !ok {
				t.Errorf("the links name the declaration %s, go-sbom doesn't list it", id)
			}
		}

		for id := range sigs {
			if _, ok := inv.digests[id]; !ok {
				t.Errorf("the links name the signature %s, go-sbom doesn't list it", id)
			}
		}
	}
}

func replacedRoot(t *testing.T) string {
	t.Helper()

	out := t.TempDir()
	write(t, out, "near/go.mod", "module example.com/near\n\ngo 1.24\n")
	write(t, out, "near/near.go", "package near\n\nfunc One() int { return 1 }\n")

	root := t.TempDir()
	write(t, root, "rep/go.mod", "module example.com/rep\n\ngo 1.24\n\nrequire example.com/near v0.0.0\n\nreplace example.com/near => "+out+"/near\n")
	write(t, root, "rep/rep.go", "package rep\n\nimport \"example.com/near\"\n\nfunc Two() int { return near.One() + 1 }\n")

	return root
}

func TestLinkSetsReadAParentWorkspace(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.work", "go 1.24\n\nuse (\n\t./mods/a\n\t./mods/b\n)\n")
	write(t, root, "mods/a/go.mod", "module example.com/a\n\ngo 1.24\n")
	write(t, root, "mods/a/a.go", "package a\n\nimport \"example.com/b\"\n\nfunc A() int { return b.B() }\n")
	write(t, root, "mods/b/go.mod", "module example.com/b\n\ngo 1.24\n")
	write(t, root, "mods/b/b.go", "package b\n\nfunc B() int { return 1 }\n")

	sets := load(t, root).LinkSets(nil)
	if len(sets) != 2 {
		t.Fatalf("%d sets, want 2", len(sets))
	}

	for _, s := range sets {
		if !slices.Contains(s.Inputs, "go.work") {
			t.Errorf("%s reads %v, not the go.work above it", s.Group, s.Inputs)
		}
	}

	for _, s := range labTree(t).LinkSets(nil) {
		if slices.ContainsFunc(s.Inputs, func(in string) bool { return strings.HasSuffix(in, "go.work") }) {
			t.Errorf("%s reads %v, with no go.work in the tree", s.Group, s.Inputs)
		}
	}
}

func TestLinkSetsAreOnePerPackage(t *testing.T) {
	sets := labTree(t).LinkSets(nil)

	by := map[string]LinkSet{}
	for _, s := range sets {
		by[s.Group] = s
	}

	svc := by["lab/svc"]
	// core.Size, which a test variable's initialiser calls, failed the
	// purity proof on its call of len: a change to its file, or a function
	// len that any file of core declares, can make it pass
	if !slices.Equal(svc.Inputs, []string{
		"lab/core/core.go", "lab/core/twice.go", "lab/go.mod", "lab/svc/ext_test.go", "lab/svc/helpers_test.go", "lab/svc/svc.go", "lab/svc/svc_test.go",
	}) {
		t.Errorf("svc inputs %v", svc.Inputs)
	}

	if !slices.Equal(svc.InputUnits, []string{"go:lab/core#api:core", "go:lab/extra#api:extra", "go:lab/svc#api:svc", "go:lab/svc/svctest#api:svctest", "go:lab/whole#api:whole"}) {
		t.Errorf("svc input units %v", svc.InputUnits)
	}

	if !slices.Equal(by["lab/core"].InputUnits, []string{"go:lab/core#api:core"}) {
		t.Errorf("core imports nothing of the tree, so its own API unit alone: %v", by["lab/core"].InputUnits)
	}

	for _, s := range sets {
		for _, l := range s.Links {
			if strings.HasPrefix(l.From, "go:") && goid.ID(l.From).Dir() != s.Group {
				t.Errorf("set %s holds a link from another package: %+v", s.Group, l)
			}
		}
	}
}

func TestLinkSetsOfOnePackage(t *testing.T) {
	for _, root := range []string{fixture(t), labRoot(t)} {
		whole := load(t, root)

		for _, dir := range sortedKeys(whole.pkgs) {
			alone, err := Load(t.Context(), root, Options{Packages: []string{dir}})
			if err != nil {
				t.Fatal(err)
			}

			got, want := alone.LinkSets([]string{dir}), whole.LinkSets([]string{dir})
			if len(got) != 1 || len(want) != 1 {
				t.Fatalf("%s: %d and %d sets", dir, len(got), len(want))
			}

			if !slices.Equal(got[0].Inputs, want[0].Inputs) || !slices.Equal(got[0].InputUnits, want[0].InputUnits) {
				t.Errorf("%s: alone %v %v, whole %v %v", dir, got[0].Inputs, got[0].InputUnits, want[0].Inputs, want[0].InputUnits)
			}

			if missing, extra := linkDiff(want[0].Links, got[0].Links), linkDiff(got[0].Links, want[0].Links); len(missing)+len(extra) > 0 {
				t.Errorf("%s alone: misses %v, adds %v", dir, missing, extra)
			}

			if len(alone.pkgs) != 1 {
				t.Errorf("%s: loaded %v from source", dir, sortedKeys(alone.pkgs))
			}
		}
	}
}

func linkDiff(a, b []Link) []Link {
	var out []Link

	for _, l := range a {
		if !slices.Contains(b, l) {
			out = append(out, l)
		}
	}

	return out
}

func TestAWholePackageBindsNoAPIUnit(t *testing.T) {
	for _, s := range labTree(t).LinkSets(nil) {
		if s.Group != "lab/whole" {
			continue
		}

		bound := 0

		for _, l := range s.Links {
			if l.From != GroupNode("lab/whole") || l.Kind != KindBind {
				continue
			}

			bound++

			if goid.ID(l.To).Kind() == goid.API {
				t.Errorf("the whole package binds its API unit: %+v", l)
			}
		}

		if bound == 0 {
			t.Fatal("lab/whole is bound whole by no link")
		}

		return
	}

	t.Fatal("no set for lab/whole")
}

func atLevel2() map[string]bool {
	on := map[string]bool{}
	for _, n := range Narrowings {
		on[n.Name] = true
	}

	return on
}

func bothWays(t *testing.T, base, head string, on map[string]bool, ss []Slice) []string {
	t.Helper()

	walked := walkSelected(t, base, head, on, ss)
	bound := selectedWith(t, base, head, slices.Sorted(maps.Keys(on)), ss, nil)[Executed]

	if !slices.Equal(walked, bound) {
		t.Errorf("under %v the walk selects %v, bind %v", slices.Sorted(maps.Keys(on)), walked, bound)
	}

	return walked
}
