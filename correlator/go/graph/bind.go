package graph

import (
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen-sbom-go/goid"
)

// Width is how far a slice's binding reaches from what it executed.
type Width int

const (
	// Executed binds a slice to the declarations it executed and what
	// coverage can't see of them: the types, constants and variables they
	// reference, closed over type definitions, functions without
	// statements, the package initialisers of the binary, and the modules
	// it links.
	Executed Width = iota
	// Dependents adds everything statically reachable from those: calls,
	// method sets and every implementation an interface call may reach,
	// transitively, up to the slice's depth.
	Dependents
)

// Widths are both widths, in order.
var Widths = []Width{Executed, Dependents}

// String is the width's name in the protocol.
func (w Width) String() string {
	if w == Dependents {
		return "dependents"
	}

	return "executed"
}

// Slice is what binding needs of one slice.
type Slice struct {
	Suite string
	Name  string
	// Sources are the slice's own files, relative to the root: its test
	// files' declarations are roots of its binding.
	Sources []string
	// Tests, when set, are the only top-level test functions of its test
	// files the slice runs.
	Tests []string
	// List is what the slice touched when it last ran, from the lists;
	// HasList is false for a slice without a list yet, which is bound to
	// everything.
	List    []string
	HasList bool
	// Depth limits the dependents width's steps; 0 is no limit.
	Depth int
}

// SetupSlice is the name, in each suite, of the binding of what the
// suite's batches ran during setup, under the setup split.
const SetupSlice = "_setup"

// The kinds of reasons a unit is bound for.
const (
	ReasonExecuted   = "executed"   // the slice's list holds it
	ReasonTest       = "test"       // declared in one of the slice's test files
	ReasonReferences = "references" // referenced by Via
	ReasonMethod     = "method"     // a method of the type Via
	ReasonImplements = "implements" // Via may call it through an interface
	ReasonInit       = "init"       // runs when the package Via starts
	ReasonWhole      = "whole"      // its package Via is bound whole
	ReasonModule     = "module"     // a module the package Via links
)

// Reason says why a unit is bound: its kind and the unit or package it
// came through.
type Reason struct {
	Kind string
	Via  string
}

// Binding is one slice's units in one width.
type Binding struct {
	// Everything marks a slice without a list: bound to every unit.
	Everything bool
	// Units are the bound units outside whole groups, with their reasons.
	Units map[string]Reason
	// Whole are the package directories bound with every unit go-sbom
	// lists for them.
	Whole []string
	// Linked are the package directories whose unbound declarations the
	// slice binds: every package its binaries link, and none while
	// unreached-new is on, which lets an unbound declaration select
	// nothing.
	Linked []string
	// Dropped are, per narrowing in force, the units and groups the wide
	// rule would add.
	Dropped map[string][]string
	// Vias are, for each bound type, constant and variable, every bound
	// declaration that references it (a type's methods count as its
	// referrers too), sorted: the whole of how it is bound, where Units
	// keeps only the first path found.
	Vias map[string][]string
}

// Result is one slice's binding in one width.
type Result struct {
	Suite, Slice string
	Width        Width
	Binding      Binding
}

// Request is what Bind binds.
type Request struct {
	Slices     []Slice
	Narrowings []string
	// Setup is, per suite, what its batches ran during setup.
	Setup map[string][]string
}

// Answer is every slice's binding in each width and, per package directory
// and width, the declarations no slice reaches, which the wide rule for new
// declarations stands on.
type Answer struct {
	Results []Result
	Unbound [2]map[string][]string
	// Narrowed are, per narrowing in force, width and package directory,
	// the declarations that narrowing alone counts as reached, kept out of
	// Unbound: the wide rule's unbound names are both together. Only
	// referenced-new adds any.
	Narrowed map[string][2]map[string][]string
}

type options struct {
	methodSets, testMethodSets, pureInit, pureFuncs, pureTestVars, moduleBumps, ifaceMethods bool
}

func optionsOf(on map[string]bool) options {
	return options{
		methodSets: on[NarrowMethodSets], testMethodSets: on[NarrowTestMethodSets], pureInit: on[NarrowPureInitialisers],
		pureFuncs:    on[NarrowPureFunctions],
		pureTestVars: on[NarrowPureTestVariables], moduleBumps: on[NarrowModuleBumps],
		ifaceMethods: on[NarrowInterfaceMethods],
	}
}

// Bind binds every slice of the request in both widths.
func (t *Tree) Bind(r Request) Answer {
	on := map[string]bool{}
	for _, n := range r.Narrowings {
		on[n] = true
	}

	ss := withSetup(r.Slices, r.Setup, on[NarrowSetupSplit])
	opts := optionsOf(on)

	var out Answer

	bound := [2]map[int32]bool{{}, {}}

	for _, s := range ss {
		if !s.HasList {
			for _, w := range Widths {
				out.Results = append(out.Results, Result{Suite: s.Suite, Slice: s.Name, Width: w, Binding: Binding{Everything: true}})
			}

			continue
		}

		b := t.bind(s, opts)
		dropped := t.dropped(s, b, on, r.Setup)

		for _, w := range Widths {
			maps.Copy(bound[w], boolSet(b[w].decls))

			if t.reachesBroken(b[w], s) {
				out.Results = append(out.Results, Result{Suite: s.Suite, Slice: s.Name, Width: w, Binding: Binding{Everything: true}})

				continue
			}

			res := Result{Suite: s.Suite, Slice: s.Name, Width: w, Binding: t.binding(b[w], dropped[w])}
			if on[NarrowUnreachedNew] {
				res.Binding.Linked = nil
			}

			out.Results = append(out.Results, res)
		}
	}

	unbound, referenced := t.unbound(bound, on[NarrowReferencedNew])
	out.Unbound = unbound

	if on[NarrowReferencedNew] {
		out.Narrowed = map[string][2]map[string][]string{NarrowReferencedNew: referenced}
	}

	return out
}

func boolSet[V any](m map[int32]V) map[int32]bool {
	out := make(map[int32]bool, len(m))
	for k := range m {
		out[k] = true
	}

	return out
}

// withSetup gives the slices what their suite's setup ran: with the setup
// split as a slice of its own per suite, without it in every slice's list.
func withSetup(ss []Slice, setup map[string][]string, split bool) []Slice {
	out := make([]Slice, 0, len(ss)+len(setup))
	done := map[string]bool{}

	for _, s := range ss {
		su := setup[s.Suite]

		switch {
		case split && len(su) > 0 && !done[s.Suite]:
			done[s.Suite] = true
			out = append(out, Slice{Suite: s.Suite, Name: SetupSlice, List: su, HasList: true})
		case !split && s.HasList:
			s.List = append(slices.Clone(s.List), su...)
		}

		out = append(out, s)
	}

	return out
}

type why struct {
	kind string
	via  int32
	pkg  string
}

type binding struct {
	decls map[int32]why
	// files are the bound files the build leaves out, each one unit.
	files map[string]why
	// sigs are the bound signature units, which no declaration stands for.
	sigs map[string]why
	// full are the types bound with their whole method set whatever the
	// narrowings: reflection or a template the analysis can't read reaches
	// them from a package the slice's binaries link.
	full   map[int32]bool
	deps   map[string]why
	linked map[string]bool
	opts   options
}

func (t *Tree) bind(s Slice, o options) [2]binding {
	b := binding{decls: map[int32]why{}, files: map[string]why{}, sigs: map[string]why{}, full: map[int32]bool{}, deps: map[string]why{}, linked: map[string]bool{}, opts: o}

	var queue []int32

	add := func(i int32, w why) {
		if _, ok := b.decls[i]; !ok {
			b.decls[i] = w
			queue = append(queue, i)
		}
	}

	var rootDirs []string

	for _, id := range s.List {
		if i, ok := t.byID[goid.ID(id)]; ok {
			add(i, why{kind: ReasonExecuted, via: -1})

			rootDirs = append(rootDirs, goid.ID(id).Dir())
		}
	}

	for _, i := range t.testRoots(s, o) {
		add(i, why{kind: ReasonTest, via: -1})
		rootDirs = append(rootDirs, t.Decls[i].ID.Dir())
	}

	testDirs := t.link(b.linked, rootDirs, s)

	// a type whose whole method set reflection or a template the analysis
	// can't read may reach, from a package the slice's binaries link, is
	// bound whole
	for _, i := range t.wholeSets(b.linked, testDirs, o) {
		b.full[i] = true
		add(i, why{kind: ReasonWhole, via: -1, pkg: t.Decls[i].ID.Dir()})
	}

	for _, dir := range sortedKeys(b.linked) {
		for _, i := range t.pkgs[dir].Decls {
			if t.Decls[i].acts(o) {
				add(i, why{kind: ReasonInit, via: -1, pkg: dir})
			}
		}

		// what its go:linkname directives pull from other packages, and
		// its tests' where the slice runs them
		pulls := t.pkgs[dir].Pulls
		if testDirs[dir] {
			pulls = slices.Concat(pulls, t.pkgs[dir].TestPulls)
		}

		for _, target := range pulls {
			if id, ok := t.purity.pullTarget(target); ok {
				if i, ok := t.byID[id]; ok {
					add(i, why{kind: ReasonInit, via: -1, pkg: dir})
				}
			}
		}
	}

	for len(queue) > 0 {
		i := queue[0]
		queue = queue[1:]

		t.expandExecuted(i, b, add)
	}

	for dir := range b.linked {
		t.bindWhole(b, dir, testDirs[dir])
	}

	t.linkModules(b, testDirs, o)

	return [2]binding{b, t.dependents(b, s.Depth)}
}

// bindWhole binds what a linked package binds whatever ran: its own
// declarations when it is whole, its test files' when they are and the
// slice runs them, and the files its build leaves out.
func (t *Tree) bindWhole(b binding, dir string, tests bool) {
	p := t.pkgs[dir]

	var ds []int32
	if p.Whole != "" {
		ds = p.Decls
	}

	// the files its build leaves out, which no recording sees
	for _, id := range p.Excluded {
		if !strings.HasSuffix(id.Name(), "_test.go") || tests {
			b.files[string(id)] = why{kind: ReasonWhole, via: -1, pkg: dir}
		}
	}

	if tests && (p.TestWhole != "" || (p.TestWholeOff != "" && !b.opts.ifaceMethods)) {
		ds = slices.Concat(ds, p.TestDecls)
	}

	for _, i := range ds {
		// bound whole: a type with its whole method set
		b.full[i] = true

		if _, ok := b.decls[i]; !ok {
			b.decls[i] = why{kind: ReasonWhole, via: -1, pkg: dir}
		}
	}
}

// linkModules binds the modules the slice's binaries link: those of the
// packages they link, or, with module-bumps off, every module that the
// main modules of those packages require.
func (t *Tree) linkModules(b binding, testDirs map[string]bool, o options) {
	for _, dir := range sortedKeys(b.linked) {
		p := t.pkgs[dir]

		mods := p.Deps
		if testDirs[dir] {
			mods = append(slices.Clone(mods), p.TestDeps...)
		}

		if !o.moduleBumps {
			mods = t.requires[p.Module]
		}

		for _, m := range mods {
			if _, ok := b.deps[m]; !ok {
				b.deps[m] = why{kind: ReasonModule, via: -1, pkg: dir}
			}
		}
	}
}

func (t *Tree) expandExecuted(i int32, b binding, add func(int32, why)) {
	w, o := b.decls[i], b.opts
	kind := t.Decls[i].ID.Kind()

	// a method reached as part of its type's method set is bound by its
	// digest only: if it ran, it is on the list itself. A test file's code
	// has no coverage, so what it reaches is followed instead.
	if w.kind == ReasonMethod && !t.Decls[i].Test {
		return
	}

	if kind == goid.Type {
		// the type's digest holds its method set; only methods without
		// statements are bound, having no coverage. A test type's methods
		// are all bound when they may run through an interface and
		// nothing records it.
		_, escapes := t.escapes[i]

		allTest := t.Decls[i].Test && (!o.testMethodSets || escapes)
		for _, m := range t.methods[i] {
			if t.Decls[m].Empty || allTest || !o.methodSets {
				add(m, why{kind: ReasonMethod, via: i})
			}
		}

		t.namedSigs(i, b)
	}

	if (kind == goid.Func || kind == goid.Method) && w.kind == ReasonReferences && !t.Decls[i].Test {
		// a function reached by reference runs only if it was recorded;
		// one without statements is bound whole, and has nothing to follow
		return
	}

	for _, sig := range t.sels[i] {
		if _, seen := b.sigs[sig]; !seen {
			b.sigs[sig] = why{kind: ReasonReferences, via: i}
		}
	}

	for _, r := range t.refs[i] {
		d := t.Decls[r]

		switch d.ID.Kind() {
		case goid.Type, goid.Const, goid.Var:
			add(r, why{kind: ReasonReferences, via: i})
		case goid.Func, goid.Method:
			if d.Empty || d.Test {
				add(r, why{kind: ReasonReferences, via: i})
			}
		case goid.Init, goid.Excluded, goid.TypeDecl, goid.MethodSig, goid.API:
		}
	}
}

// namedSigs binds the signatures of a type's methods that some interface
// or lookup by name reaches: its declaration's links in the names set.
// While the narrowing is off the type's own unit holds them all anyway.
func (t *Tree) namedSigs(i int32, b binding) {
	if !t.hasDecl(i) {
		return
	}

	for _, m := range t.methods[i] {
		if sig, ok := t.sigID(m); ok && t.names.has(methodName(t.Decls[m])) {
			if _, seen := b.sigs[sig]; !seen {
				b.sigs[sig] = why{kind: ReasonMethod, via: i}
			}
		}
	}
}

func (t *Tree) dependents(e binding, depth int) binding {
	// at the dependents width every method of a type is bound: its whole
	// unit stands for it
	opts := e.opts
	opts.ifaceMethods = false

	b := binding{decls: make(map[int32]why, len(e.decls)), files: e.files, sigs: maps.Clone(e.sigs), full: e.full, deps: e.deps, linked: e.linked, opts: opts}
	steps := map[int32]int{}

	queue := make([]int32, 0, len(e.decls))
	for _, i := range sortedKeys(e.decls) {
		b.decls[i] = e.decls[i]
		queue = append(queue, i)
	}

	add := func(i int32, w why) {
		if _, ok := b.decls[i]; ok {
			return
		}

		if !e.linked[t.Decls[i].ID.Dir()] && !t.Decls[i].Test {
			return
		}

		b.decls[i] = w
		steps[i] = steps[w.via] + 1
		queue = append(queue, i)
	}

	for len(queue) > 0 {
		i := queue[0]
		queue = queue[1:]

		if depth > 0 && steps[i] >= depth {
			continue
		}

		for _, r := range t.refs[i] {
			add(r, why{kind: ReasonReferences, via: i})
		}

		for _, sig := range t.sels[i] {
			if _, seen := b.sigs[sig]; !seen {
				b.sigs[sig] = why{kind: ReasonReferences, via: i}
			}
		}

		for _, r := range t.impls[i] {
			add(r, why{kind: ReasonImplements, via: i})
		}

		if t.Decls[i].ID.Kind() == goid.Type {
			for _, m := range t.methods[i] {
				add(m, why{kind: ReasonMethod, via: i})
			}
		}
	}

	return b
}

// acts tells whether a declaration may act when its package or test
// binary starts, so every slice linking it binds it: an init, a variable
// whose initialiser calls something not known to be pure or can panic,
// and, with pure-test-variables off, every test file variable with an
// initialiser.
func (d *Decl) acts(o options) bool {
	switch {
	case d.Valued && !o.pureTestVars:
		return true
	case !o.pureInit:
		return d.InitAny
	case !o.pureFuncs:
		return d.InitList
	default:
		return d.Init
	}
}

// testRoots are the declarations of the slice's test files it runs, and
// what runs in every test binary of their packages (TestMain, inits,
// initialisers).
func (t *Tree) testRoots(s Slice, o options) []int32 {
	var out []int32

	dirs := map[string]bool{}

	for _, f := range s.Sources {
		f = path.Clean(f)

		decls, ok := t.testFiles[f]
		if !ok {
			continue
		}

		dirs[path.Dir(f)] = true

		for _, i := range decls {
			if len(s.Tests) > 0 && isTestFunc(t.Decls[i]) && !slices.Contains(s.Tests, testName(t.Decls[i])) {
				continue
			}

			out = append(out, i)
		}
	}

	for file, decls := range t.testFiles {
		if !dirs[path.Dir(file)] {
			continue
		}

		for _, i := range decls {
			if d := t.Decls[i]; d.acts(o) || testName(d) == testMain || d.ID.Kind() == goid.Init {
				out = append(out, i)
			}
		}
	}

	slices.Sort(out)

	return slices.Compact(out)
}

const testMain = "TestMain"

func testName(d *Decl) string {
	if d.ID.Kind() != goid.Func {
		return ""
	}

	n := d.ID.Name()
	n = strings.TrimSuffix(n, goid.XTestSuffix)

	return strings.TrimSuffix(n, goid.TestSuffix)
}

func isTestFunc(d *Decl) bool {
	n := testName(d)
	for _, p := range []string{"Test", "Benchmark", "Fuzz", "Example"} {
		if strings.HasPrefix(n, p) && n != testMain {
			return true
		}
	}

	return false
}

// link adds the packages a slice's binaries link: those it executed or
// tests, and everything they import within the tree. It returns the
// directories whose tests the slice runs.
func (t *Tree) link(linked map[string]bool, dirs []string, s Slice) map[string]bool {
	var visit func(dir string, test bool)

	visit = func(dir string, test bool) {
		p := t.pkgs[dir]
		if p == nil || (linked[dir] && !test) {
			return
		}

		linked[dir] = true

		for _, d := range p.Imports {
			visit(d, false)
		}

		if test {
			for _, d := range p.TestImports {
				visit(d, false)
			}
		}
	}

	testDirs := map[string]bool{}

	for _, f := range s.Sources {
		if strings.HasSuffix(f, "_test.go") {
			testDirs[path.Dir(path.Clean(f))] = true
		}
	}

	for _, d := range dirs {
		visit(d, testDirs[d])
	}

	for d := range testDirs {
		visit(d, true)
	}

	return testDirs
}
