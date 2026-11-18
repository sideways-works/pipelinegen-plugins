package graph

import (
	"go/types"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/sideways-works/pipelinegen-sbom-go/goid"
)

// The typed link kinds, as the planner walks them: a follow link binds its
// target and carries on from it, a bind link binds it and stops, and a
// dependents-only link counts only at the dependents width.
const (
	KindFollow         = "follow"
	KindBind           = "bind"
	KindDependentsOnly = "dependents-only"
)

// While a narrowing is off, a wide link holds; a narrow one replaces it
// while the narrowing is on.
const (
	WhileOff = "off"
	WhileOn  = "on"
)

type Link struct {
	From, To  string
	Kind      string
	Narrowing string
	While     string
	// IfLinked: it holds only for slices whose binaries link the group
	// of To.
	IfLinked bool
	// Ref marks a link a reference in From's syntax made, whatever its
	// kind: a name a bound declaration references counts as reached under
	// referenced-new, a method of a type it uses or an implementation
	// doesn't.
	Ref bool
}

type LinkSet struct {
	Group string
	// Inputs are the package's files, its module's go.mod and the go.work
	// above it.
	Inputs []string
	// InputUnits are the API units of the tree's packages it imports, directly
	// or not, tests included, and its own: every API unit is an input unit,
	// counted among no group's names.
	InputUnits []string
	Links      []Link
	// CannotTell are the units of a package that doesn't build: what
	// reaches them is bound wide.
	CannotTell []CannotTell
}

type CannotTell struct {
	Unit, Reason string
}

// GroupNode is the planner's node for a package: a link into it says the
// package is linked into the binary.
func GroupNode(dir string) string { return "group:" + dir }

// TestNode is the junction of a directory's test binary, which links
// more than its package: its tests' imports and what runs before them.
func TestNode(dir string) string { return "node:go:test:" + dir }

// ModuleNode is the junction of every module a main module requires: the
// wide rule for module bumps.
func ModuleNode(modulePath string) string { return "node:go:module:" + modulePath }

// ImplNode is the meeting point of an interface method's callers and the
// methods that may implement it, by name and signature shape; a generic
// type's methods meet callers by name alone.
func ImplNode(key string) string { return "node:go:impl:" + key }

// LinkSets answers the links of the packages in dirs (every package when
// nil), one set each.
func (t *Tree) LinkSets(dirs []string) []LinkSet {
	if dirs == nil {
		dirs = sortedKeys(t.pkgs)
	}

	out := make([]LinkSet, 0, len(dirs))

	for _, dir := range dirs {
		p := t.pkgs[dir]
		if p == nil {
			continue
		}

		s := LinkSet{Group: dir, Inputs: t.setInputs(p), InputUnits: t.inputUnits(p)}
		l := &linker{t: t, seen: map[Link]bool{}}

		l.pkg(p)

		s.Links = l.links
		slices.SortFunc(s.Links, compareLinks)

		for _, id := range p.Units {
			s.CannotTell = append(s.CannotTell, CannotTell{Unit: string(id), Reason: p.Broken})
		}

		out = append(out, s)
	}

	return out
}

func compareLinks(a, b Link) int {
	for _, c := range [][2]string{{a.From, b.From}, {a.To, b.To}, {a.Kind, b.Kind}, {a.Narrowing, b.Narrowing}, {a.While, b.While}} {
		if n := strings.Compare(c[0], c[1]); n != 0 {
			return n
		}
	}

	// two links alike but for these flags still sort the same every time
	for _, c := range [][2]bool{{a.IfLinked, b.IfLinked}, {a.Ref, b.Ref}} {
		if c[0] != c[1] {
			if c[0] {
				return 1
			}

			return -1
		}
	}

	return 0
}

// linker collects one package's links, each once; a reference and another
// link with the same ends and tags merge, marked as a reference.
type linker struct {
	t     *Tree
	links []Link
	seen  map[Link]bool
}

func (l *linker) add(k Link) {
	ref := k.Ref
	k.Ref = false

	if l.seen[k] {
		if ref {
			for i := range l.links {
				if same := l.links[i]; same.From == k.From && same.To == k.To && same.Kind == k.Kind &&
					same.Narrowing == k.Narrowing && same.While == k.While && same.IfLinked == k.IfLinked {
					l.links[i].Ref = true
				}
			}
		}

		return
	}

	l.seen[k] = true
	k.Ref = ref
	l.links = append(l.links, k)
}

// id is the unit a reference to a declaration binds: a test type that
// doesn't escape is bound by its own declaration, its typedecl unit.
func (t *Tree) id(i int32) string {
	if t.hasTypeDecl(i) || t.hasDecl(i) {
		return t.twin(i)
	}

	return string(t.Decls[i].ID)
}

// hasDecl tells whether a type outside the tests has methods, so go-sbom
// lists its declaration (typedecl) and a signature unit per method apart.
func (t *Tree) hasDecl(i int32) bool {
	d := t.Decls[i]

	return !d.Test && d.ID.Kind() == goid.Type && (len(t.methods[i]) > 0 || t.testMethods[i])
}

func (t *Tree) hasTypeDecl(i int32) bool {
	_, escapes := t.escapes[i]

	return t.hasTwin(i) && !escapes
}

// hasTwin tells whether go-sbom lists a typedecl unit beside a test type:
// it does for every test type with methods, from syntax alone, and marks
// the type itself as no root, so the slice declaring it roots at the twin.
func (t *Tree) hasTwin(i int32) bool {
	d := t.Decls[i]

	return d.Test && d.ID.Kind() == goid.Type && len(t.methods[i]) > 0
}

func (t *Tree) twin(i int32) string {
	d := t.Decls[i]

	return string(goid.New(d.ID.Dir(), goid.TypeDecl, d.ID.Name()))
}

func (l *linker) pkg(p *Package) {
	t := l.t
	group := GroupNode(p.Dir)

	for _, i := range slices.Concat(p.Decls, p.TestDecls) {
		l.decl(i)
	}

	// linking: a declaration links its package, a package what it
	// imports, and what runs when it starts
	for _, i := range p.Decls {
		l.add(Link{From: string(t.Decls[i].ID), To: group, Kind: KindFollow})
	}

	for _, dir := range p.Imports {
		l.add(Link{From: group, To: GroupNode(dir), Kind: KindFollow})
	}

	for _, i := range p.Decls {
		l.starts(group, i, false)
	}

	l.pulls(group, p.Pulls)
	l.pulls(TestNode(p.Dir), p.TestPulls)

	// a package that doesn't build: every unit go-sbom lists for it is
	// bound with it, and can't be told; with only its tests failing, with
	// its test binary
	from := group
	if p.BrokenTests {
		from = TestNode(p.Dir)
	}

	for _, id := range p.Units {
		l.add(Link{From: from, To: string(id), Kind: KindBind})
	}

	if p.Whole != "" {
		// every unit go-sbom lists for the package's own code, as a whole
		// group binds
		l.whole(group, p.Decls)
	}

	// a file the build leaves out: no recording sees its declarations, so
	// it binds every slice that links the package, or, a test file, every
	// slice of its test binary
	for _, id := range p.Excluded {
		from := group
		if strings.HasSuffix(id.Name(), "_test.go") {
			from = TestNode(p.Dir)
		}

		l.add(Link{From: from, To: string(id), Kind: KindBind})
	}

	l.testWhole(p)

	l.modules(group, p, p.OwnDeps)

	for _, k := range t.extra[p.Dir] {
		l.add(k)
	}

	if len(p.TestDecls) == 0 {
		return
	}

	// a test binary links its package, its tests' imports, and runs
	// TestMain, the test files' inits and their variables that may act
	test := TestNode(p.Dir)

	for _, i := range p.TestDecls {
		l.add(Link{From: t.id(i), To: test, Kind: KindFollow})
	}

	l.add(Link{From: test, To: group, Kind: KindFollow})

	for _, dir := range p.TestImports {
		l.add(Link{From: test, To: GroupNode(dir), Kind: KindFollow})
	}

	for _, i := range p.TestDecls {
		l.starts(test, i, true)
	}

	l.modules(test, p, p.OwnTestDeps)
}

func (l *linker) testWhole(p *Package) {
	switch {
	case p.TestWhole != "":
		l.whole(TestNode(p.Dir), p.TestDecls)
	case p.TestWholeOff != "":
		// while interface-methods, whose per-site rule reads a test file's
		// lookup by name, is off
		l.whole(TestNode(p.Dir), p.TestDecls, NarrowInterfaceMethods)
	}
}

// whole binds every unit go-sbom lists for the declarations from one node;
// with a narrowing named, only while it is off.
func (l *linker) whole(from string, ds []int32, offWith ...string) {
	t := l.t

	bind := func(to string) {
		k := Link{From: from, To: to, Kind: KindBind}
		if len(offWith) > 0 {
			k.Narrowing, k.While = offWith[0], WhileOff
		}

		l.add(k)
	}

	for _, i := range ds {
		bind(string(t.Decls[i].ID))

		if t.hasTwin(i) || t.hasDecl(i) {
			bind(t.twin(i))
		}

		for _, m := range t.methods[i] {
			if sig, ok := t.sigID(m); ok && !t.Decls[m].Test {
				bind(sig)
			}
		}
	}
}

// starts links what runs when a package or a test binary starts: an init,
// a test's TestMain, a variable whose initialiser may act, one that calls
// a function of the tree proved pure while pure-functions is off, one that
// calls anything while pure-initialisers is off, and in a test file any
// initialiser while pure-test-variables is off.
func (l *linker) starts(from string, i int32, test bool) {
	d := l.t.Decls[i]
	to := l.t.id(i)

	if d.ID.Kind() == goid.Init || (test && testName(d) == testMain) || d.Init {
		l.add(Link{From: from, To: to, Kind: KindFollow})

		return
	}

	// Init implies InitList, which implies InitAny
	if d.InitList {
		l.add(Link{From: from, To: to, Kind: KindFollow, Narrowing: NarrowPureFunctions, While: WhileOff})
	}

	if d.InitAny {
		l.add(Link{From: from, To: to, Kind: KindFollow, Narrowing: NarrowPureInitialisers, While: WhileOff})
	}

	if test && d.Valued {
		l.add(Link{From: from, To: to, Kind: KindFollow, Narrowing: NarrowPureTestVariables, While: WhileOff})
	}
}

// pulls links a package, or its test binary, to the declarations of other
// packages of the tree its go:linkname directives name: it reads or calls
// them without a reference, and what runs where it is linked may use them.
func (l *linker) pulls(from string, targets []string) {
	for _, target := range targets {
		if id, ok := l.t.purity.pullTarget(target); ok {
			l.add(Link{From: from, To: string(id), Kind: KindFollow})
		}
	}
}

// modules links the modules a package's or a test binary's own imports
// bring in, while module-bumps is on; while it is off, every module its
// main module requires, through one junction per main module.
func (l *linker) modules(from string, p *Package, own []string) {
	// a module replaced by a directory is code go-sbom lists no unit for:
	// a link to it would name nothing, and the planner would bind the
	// whole package instead
	listed := func(m string) bool { return !l.t.dirReplaced[p.Module][m] }

	for _, m := range own {
		if listed(m) {
			l.add(Link{From: from, To: goid.Dep(m), Kind: KindBind, Narrowing: NarrowModuleBumps, While: WhileOn})
		}
	}

	mod := l.t.modulePath(p.Module)
	if mod == "" {
		return
	}

	l.add(Link{From: from, To: ModuleNode(mod), Kind: KindFollow, Narrowing: NarrowModuleBumps, While: WhileOff})

	for _, m := range l.t.requires[p.Module] {
		if listed(m) {
			l.add(Link{From: ModuleNode(mod), To: goid.Dep(m), Kind: KindBind})
		}
	}
}

func (l *linker) decl(i int32) {
	t := l.t
	d := t.Decls[i]
	from := t.id(i)

	for _, r := range t.refs[i] {
		l.add(Link{From: from, To: t.id(r), Kind: refKind(t.Decls[r]), Ref: true})

		if from != string(d.ID) {
			l.add(Link{From: string(d.ID), To: t.id(r), Kind: refKind(t.Decls[r]), Ref: true})
		}
	}

	// a selection binds the signature of the method it resolves to
	for _, sig := range t.sels[i] {
		l.add(Link{From: from, To: sig, Kind: KindBind, Ref: true})
	}

	if d.ID.Kind() == goid.Type {
		l.methods(i)
	}

	if t.hasDecl(i) {
		// the whole type, its method set in its digest, while the narrowing
		// is off; the signatures some interface or lookup by name reaches
		// are the names set's links, since they depend on the whole
		// program; at the dependents width every method counts anyway
		l.add(Link{From: from, To: string(d.ID), Kind: KindFollow, Narrowing: NarrowInterfaceMethods, While: WhileOff})
		l.add(Link{From: from, To: string(d.ID), Kind: KindDependentsOnly})
	}

	if t.hasTwin(i) && !t.hasTypeDecl(i) {
		// a type that escapes to an interface is bound whole at every
		// level, also where the slice declaring it roots at its twin
		l.add(Link{From: t.twin(i), To: string(d.ID), Kind: KindFollow})
	}

	if t.hasTypeDecl(i) {
		// the full type comes back when either narrowing is off
		for _, n := range []string{NarrowMethodSets, NarrowTestMethodSets} {
			l.add(Link{From: from, To: string(d.ID), Kind: KindFollow, Narrowing: n, While: WhileOff})
		}
	}

	for _, key := range t.calls[i] {
		l.add(Link{From: from, To: ImplNode(key), Kind: KindDependentsOnly})
	}

	if key, ok := t.implKeys[i]; ok {
		for _, k := range key {
			l.add(Link{From: ImplNode(k), To: string(d.ID), Kind: KindDependentsOnly, IfLinked: true})
		}
	}
}

// refKind is how a reference binds its target: a type, a constant, a
// variable or test code is bound and followed; a function without
// statements is bound, and has nothing to follow; a function with
// statements runs only if it was recorded, so it counts at the dependents
// width only.
func refKind(d *Decl) string {
	switch d.ID.Kind() {
	case goid.Func, goid.Method:
		switch {
		case d.Test:
			return KindFollow
		case d.Empty:
			return KindBind
		default:
			return KindDependentsOnly
		}
	case goid.Type, goid.Const, goid.Var, goid.Init, goid.Excluded, goid.TypeDecl, goid.MethodSig, goid.API:
	}

	return KindFollow
}

// methods links a type to its methods. Executed: a method without
// statements always, one with statements while method-sets is off, and a
// test type's every method while test-method-sets is off or it escapes to
// an interface; a test method is followed, since test code has no
// coverage. Dependents: every method.
func (l *linker) methods(i int32) {
	t := l.t
	d := t.Decls[i]
	_, escapes := t.escapes[i]

	from := []string{string(d.ID)}
	if t.id(i) != string(d.ID) {
		from = append(from, t.id(i))
	}

	for _, m := range t.methods[i] {
		md := t.Decls[m]
		to := string(md.ID)

		kind := KindBind
		if md.Test {
			kind = KindFollow
		}

		for _, f := range from {
			switch {
			case md.Empty || (d.Test && escapes):
				l.add(Link{From: f, To: to, Kind: kind})
			default:
				l.add(Link{From: f, To: to, Kind: kind, Narrowing: NarrowMethodSets, While: WhileOff})

				if d.Test {
					l.add(Link{From: f, To: to, Kind: kind, Narrowing: NarrowTestMethodSets, While: WhileOff})
				}
			}

			l.add(Link{From: f, To: to, Kind: KindDependentsOnly})
		}
	}
}

func (t *Tree) modulePath(dir string) string {
	for _, m := range t.mods {
		if m.Dir == dir {
			return m.Path
		}
	}

	return ""
}

func (t *Tree) setInputs(p *Package) []string {
	out := slices.Clone(p.Files)

	for _, id := range p.Excluded {
		out = append(out, path.Join(p.Dir, id.Name()))
	}

	out = append(out, path.Join(p.Module, "go.mod"))

	if w := t.works[p.Module]; w != "" {
		out = append(out, w)
	}

	// the files whose syntax proved, or failed to prove, the functions its
	// initialisers call pure: a change to one can change what its
	// variables bind, and asking about one answers this set too (Readers)
	for f := range t.pureInputs[p.Dir] {
		out = append(out, f)
	}

	slices.Sort(out)

	return slices.Compact(out)
}

// inputUnits are the API units of the tree's packages a package imports,
// directly or not, tests included, and its own.
func (t *Tree) inputUnits(p *Package) []string {
	seen := map[string]bool{}

	var visit func(dir string)

	visit = func(dir string) {
		if seen[dir] {
			return
		}

		seen[dir] = true

		for _, d := range t.listed[dir].Imports {
			visit(d)
		}
	}

	seen[p.Dir] = true

	for _, d := range slices.Concat(p.Imports, p.TestImports) {
		visit(d)
	}

	out := []string{}

	for _, dir := range sortedKeys(seen) {
		if name := t.listed[dir].Name; name != "" {
			out = append(out, string(goid.New(dir, goid.API, name)))
		}
	}

	return out
}

// implKey is a method's meeting point with interface calls: its name and
// the counts of its parameters and results, and whether it is variadic.
func implKey(name string, sig *types.Signature) string {
	key := name + "/" + strconv.Itoa(sig.Params().Len()) + "/" + strconv.Itoa(sig.Results().Len())
	if sig.Variadic() {
		key += "/..."
	}

	return key
}
