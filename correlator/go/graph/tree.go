// Package graph reads a Go repository with the type checker and records who
// uses what, naming every declaration by goid as go-sbom lists it.
package graph

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"go/version"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/tools/go/packages"

	"github.com/sideways-works/pipelinegen-sbom-go/decls"
	"github.com/sideways-works/pipelinegen-sbom-go/goid"
)

// Decl is one top-level declaration of a tree.
type Decl struct {
	ID   goid.ID
	File string
	// Test marks a declaration of a _test.go file. It has no coverage: slices
	// reach it as a root or through references.
	Test  bool
	Empty bool
	// Init marks what may act when its package is initialised: an init function,
	// or a variable whose initialiser may call something with effects or panic.
	// InitList counts only the configured pure calls as free of effects, InitAny
	// none; Init implies InitList, which implies InitAny.
	Init, InitList, InitAny bool
	// Valued marks a test file's variable with an initialiser: it is set
	// up for every test of its package, whatever its initialiser calls.
	Valued bool
	Recv   string
	// node is the syntax whose identifiers count as the declaration's
	// references, while the tree is being read.
	node ast.Node
}

// Tree is one checkout's declarations and the references between them.
type Tree struct {
	Root  string
	Decls []*Decl
	// Uncacheable says why the answer depends on the machine, when it
	// does: a module the go command run there is too old to read, bound
	// wide on its own.
	Uncacheable string
	// GoVersions are the versions of the go command run in each loaded
	// module's directory, by directory: a shim can pick a different go in
	// each.
	GoVersions map[string]string
	byID       map[goid.ID]int32
	// refs are the declarations each declaration's syntax names, or whose
	// types its variables have.
	refs [][]int32
	// impls are the methods each declaration may reach through calls of
	// interface methods: every implementation in the tree.
	impls [][]int32
	// calls are, per declaration, the meeting points of the interface
	// methods it calls; implKeys, per method, those it may implement.
	calls, implKeys map[int32][]string
	methods         map[int32][]int32
	// extra are, per package directory, the links of its reach junctions:
	// what its code hands outside the tree, what its types' values reach,
	// what its reflection reaches.
	extra map[string][]Link
	// junctions are the follow links of the reach junctions and of the
	// names set, by their source, for bind.
	junctions map[string][]string
	// forced say, per junction, what makes it reach every type's whole
	// method set: a value whose type is an interface without methods.
	forced map[string][]string
	// executes say where the tree executes a template whose text the scan
	// did not read, and why it counts so: what such a call hands its
	// template binds its whole method set.
	executes []string
	resolver *resolver
	// testMethods are the types of packages read from export data with a
	// method that only a test file of theirs declares.
	testMethods map[int32]bool
	// names are N, for bind; nil binds every signature.
	names *Names
	// sels are, per declaration, the signature units of the methods its
	// selections resolve to.
	sels      map[int32][]string
	pkgs      map[string]*Package
	testFiles map[string][]int32
	mods      modules
	// requires are the modules each main module's go.mod requires, by its
	// directory: the wide rule for module bumps.
	requires map[string][]string
	// dirReplaced are, by a main module's directory, the modules its
	// go.mod replaces with a directory: code no sbom lists as a module.
	dirReplaced map[string]map[string]bool
	// works are, by a main module's directory, the go.work the go command
	// finds for it, if any: it decides the module's dependencies too.
	works    map[string]string
	pure     map[string]bool
	typePkgs []*types.Package
	// purity proves functions of the tree pure, for initialisers that
	// call them; pending are the variables whose initialisers wait for it,
	// pureInputs, by package directory, the files its proofs read, and
	// misplaced the import paths the go command builds from another
	// directory than their module path names
	purity     *purity
	pending    []pendingVar
	pureInputs map[string]map[string]bool
	misplaced  map[string]bool
	// escapes are the test types whose values reach an interface: their
	// methods may run without a call naming them. The value is where the
	// first such use was seen, for people.
	escapes map[int32]string
	// syntax are the packages loaded from source; external, the tree's
	// packages read from export data.
	syntax   map[*types.Package]bool
	external map[*types.Package]*external
	build    decls.Build
	// listed are the tree's packages as the go command lists them, by
	// directory: their names and the tree's packages they import.
	listed map[string]listedPackage
	// unparsed are the package directories with a file that doesn't
	// parse: their declarations can't be named, so none is read.
	unparsed map[string]bool
	// embeds are, for each test type, the test types its struct embeds:
	// they escape when it does, since their methods are promoted into its
	// method set.
	embeds map[int32][]int32
}

type pendingVar struct {
	decl *Decl
	dir  string
	spec *ast.ValueSpec
	info *types.Info
}

// Package is one package directory of the tree.
type Package struct {
	Dir       string
	Module    string // the module's directory
	Name      string
	Decls     []int32
	TestDecls []int32
	// Imports are the directories of the tree's packages the package
	// imports; TestImports add those its tests import.
	Imports, TestImports []string
	// Deps and TestDeps are the modules of every package the package
	// links, transitively, without and with its tests.
	Deps, TestDeps []string
	// OwnDeps and OwnTestDeps are the modules it links without going
	// through another package of the tree, without and with its tests.
	OwnDeps, OwnTestDeps []string
	// Whole says why the package's own declarations are bound as a whole,
	// when they are: a reason found in its non-test code.
	Whole string
	// TestWhole says why its test files' declarations are bound as a whole,
	// when a test file or a broken package gives a reason; a reason in non-test
	// code leaves them alone.
	TestWhole string
	// TestWholeOff says why its test files' declarations are bound as a
	// whole while interface-methods is off: a lookup by name in a test
	// file, which the per-site rule reads while it is on.
	TestWholeOff string
	// Excluded are the units of the files the build leaves out.
	Excluded []goid.ID
	// Pulls and TestPulls are the declarations of other packages that the
	// linkname directives of its own and its test files name, as
	// import/path.Name: they read or call them without a reference.
	Pulls, TestPulls []string
	// Files are its Go files and the other files the go command lists
	// for it, its tests' and the ones the build leaves out among them.
	Files []string
	// Broken says why the package doesn't build, when it doesn't: it has
	// no file in the build, a file that doesn't parse, or code the type
	// checker refuses. Such a package is bound wide on its own.
	Broken string
	// BrokenTests says only its tests' variants fail: the package builds
	// for the code that imports it.
	BrokenTests bool
	// Units are, for a broken package, the units go-sbom lists for it that
	// can't be told: its declarations, or its files when one doesn't
	// parse; only its tests' when only those fail.
	Units []goid.ID
}

// ErrLoad wraps a tree that can't be read.
var ErrLoad = errors.New("load")

// ErrToolchain is a go command older than a module of the tree asks for.
// The type checker can't read such a tree; the answer depends on the
// machine, not on the tree, so it must not be cached.
var ErrToolchain = errors.New("toolchain")

const loadMode = packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
	packages.NeedImports | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo |
	packages.NeedModule

type work struct {
	info  *types.Info
	decls []*Decl
	file  *ast.File
	test  bool
}

// Options say what Load reads.
type Options struct {
	// Modules are the directories of the Go modules to read, relative to
	// the root; none reads every go.mod of the tree.
	Modules []string
	// PureCalls are the functions an initialiser may call and still be
	// bound only through references, as "package/path.Name"; nil is
	// DefaultPureCalls.
	PureCalls []string
	// Build is the build the tests run with, the same go-sbom reads.
	Build decls.Build
	// Packages, when set, are the only package directories loaded from
	// source; the tree's other packages are read from export data, and
	// their declarations from their files without a type check.
	Packages []string
	// Claimed, when set, are the Go files the core passed as claimed. A
	// package that doesn't build and has none of them was left out by the
	// repository's config, and is left out here too.
	Claimed map[string]bool
}

// Load reads the Go modules of o with the type checker, test files
// included.
func Load(ctx context.Context, root string, o Options) (*Tree, error) {
	// resolved, so the go command reports files under the same root the
	// walk finds modules in
	root, err := decls.Root(root)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLoad, err)
	}

	mods, err := findModules(root)
	if err != nil {
		return nil, err
	}

	if len(o.Modules) == 0 {
		for _, m := range mods {
			if o.Packages == nil || slices.ContainsFunc(o.Packages, func(d string) bool { md, _ := mods.moduleOf(d); return md == m.Dir }) {
				o.Modules = append(o.Modules, m.Dir)
			}
		}
	}

	versions, tooOld, err := checkToolchain(ctx, root, mods, o)
	if err != nil {
		return nil, err
	}

	t := newTree(root, mods, o)
	t.GoVersions = versions
	fset := token.NewFileSet()
	idx := goid.NewIndex()
	seenFile := map[string]bool{}
	env := Env(o.Build)

	var todo []work

	for _, md := range o.Modules {
		pats := patterns(mods, md, o.Packages)
		if o.Packages != nil && len(pats) == 0 {
			// none of the asked packages is in this module; loading no
			// pattern would load its root directory as a package
			continue
		}

		if why := tooOld[md]; why != "" {
			// the machine's go command, not the tree: this module is wide
			// and the answer isn't cached; the other modules stay exact
			t.Uncacheable = why
			if err := t.brokenModule(md, why, o); err != nil {
				return nil, err
			}

			continue
		}

		w, err := t.loadModule(ctx, md, pats, o, env, fset, idx, seenFile)
		if err != nil {
			return nil, err
		}

		todo = append(todo, w...)

		if w := workOf(root, md); w != "" {
			t.works[md] = w
		}
	}

	t.linkMethods()

	if err := t.excluded(o); err != nil {
		return nil, err
	}

	t.relate(fset, idx, todo)

	return t, nil
}

// loadModule loads one module's packages and what they link. A failure of
// the module itself binds its packages wide; only one that depends on the
// machine (the module cache) fails the load.
func (t *Tree) loadModule(ctx context.Context, md string, pats []string, o Options, env []string,
	fset *token.FileSet, idx *goid.Index, seenFile map[string]bool,
) ([]work, error) {
	cfg := &packages.Config{
		Context: ctx, Mode: loadMode, Dir: filepath.Join(t.Root, md), Fset: fset, Tests: true,
		Env: env, BuildFlags: BuildFlags(o.Build),
	}

	pkgs, err := packages.Load(cfg, pats...)
	if err != nil {
		return nil, t.brokenModule(md, t.firstLine(err.Error(), md), o)
	}

	w, err := t.addModule(pkgs, md, o, fset, idx, seenFile)

	if mod, ok := errors.AsType[*moduleError](err); ok {
		return nil, t.brokenModule(md, mod.why, o)
	}

	if err != nil {
		return nil, err
	}

	if err := t.moduleDeps(ctx, md, env, o.Build, pats); err != nil {
		return w, t.brokenModule(md, t.firstLine(err.Error(), md), o)
	}

	if err := t.readRequires(md); err != nil {
		return w, t.brokenModule(md, t.firstLine(err.Error(), md), o)
	}

	return w, nil
}

func (t *Tree) addModule(pkgs []*packages.Package, md string, o Options, fset *token.FileSet, idx *goid.Index, seenFile map[string]bool) ([]work, error) {
	broken, err := t.brokenPackages(pkgs, md, o.Build)
	if err != nil {
		return nil, fmt.Errorf("%w %s: %w", ErrLoad, md, err)
	}

	var todo []work

	for _, p := range pkgs {
		todo = append(todo, t.addPackage(p, md, fset, idx, seenFile)...)
	}

	if err := t.gatedDirs(md, broken, o.Packages); err != nil {
		return nil, err
	}

	t.dropUnclaimed(broken, o.Claimed)

	if err := t.addBroken(md, broken, o.Build); err != nil {
		return nil, err
	}

	return todo, nil
}

func patterns(mods modules, dir string, asked []string) []string {
	if asked == nil {
		return []string{"./..."}
	}

	var out []string

	for _, d := range asked {
		if md, ok := mods.moduleOf(d); ok && md == dir {
			rel := strings.TrimPrefix(strings.TrimPrefix(d, dir), "/")
			if dir == "." {
				rel = d
			}

			out = append(out, "./"+rel)
		}
	}

	return out
}

func (t *Tree) relate(fset *token.FileSet, idx *goid.Index, todo []work) {
	t.refs = make([][]int32, len(t.Decls))
	t.impls = make([][]int32, len(t.Decls))
	t.escapes = map[int32]string{}
	t.embeds = map[int32][]int32{}
	t.sels = map[int32][]string{}
	t.extra = map[string][]Link{}
	t.forced = map[string][]string{}
	t.executes = nil
	calls := map[*types.Func][]int32{}

	t.resolveInits()

	t.resolver = newResolver(t, fset, idx, todo)
	defer func() { t.resolver = nil }()

	for _, w := range todo {
		t.fileRefs(fset, idx, w, calls)

		if w.test {
			t.testEscapes(fset, idx, w)
		}

		t.handoffs(fset, idx, w)
	}

	t.implementations(fset, idx, calls)
	t.promoteEscapes()
	t.aliasMethods()

	// declarations of packages read from export data joined on the way
	for len(t.refs) < len(t.Decls) {
		t.refs = append(t.refs, nil)
		t.impls = append(t.impls, nil)
	}

	for _, d := range t.Decls {
		d.node = nil
	}
}

// aliasMethods makes a type reference each alias of it that declares methods:
// go-sbom keys such a method by the alias receiver as written, so code naming
// only the aliased type would otherwise never bind them.
func (t *Tree) aliasMethods() {
	for i, d := range t.Decls {
		alias := int32(i)

		ts, ok := d.node.(*ast.TypeSpec)
		if !ok || !ts.Assign.IsValid() || len(t.methods[alias]) == 0 {
			continue
		}

		target, ok := t.aliasTarget(d.ID.Dir(), ts, map[string]bool{})
		if ok && target != alias && !slices.Contains(t.refs[target], alias) {
			t.refs[target] = append(t.refs[target], alias)
		}
	}
}

// aliasTarget is the type of the package an alias declaration names, through
// other aliases of the package; methods can be declared only on such a type.
func (t *Tree) aliasTarget(dir string, ts *ast.TypeSpec, seen map[string]bool) (int32, bool) {
	x := ast.Unparen(ts.Type)
	if star, ok := x.(*ast.StarExpr); ok {
		// type P = *Amount; func (P) M() declares a pointer method of Amount
		x = ast.Unparen(star.X)
	}

	id, ok := x.(*ast.Ident)
	if !ok || seen[id.Name] {
		return 0, false
	}

	seen[id.Name] = true

	i, ok := t.byID[goid.New(dir, goid.Type, id.Name)]
	if !ok {
		return 0, false
	}

	if next, ok := t.Decls[i].node.(*ast.TypeSpec); ok && next.Assign.IsValid() {
		if j, ok := t.aliasTarget(dir, next, seen); ok {
			return j, true
		}
	}

	return i, true
}

// Env is the environment of every go command the correlator runs: the fixed
// build go-sbom reads, the module cache only, no network and no toolchain
// switch. -trimpath lets base and head trees in different directories share
// compiled export data.
func Env(b decls.Build) []string {
	ctx := b.Context()

	cgo := "0"
	if ctx.CgoEnabled {
		cgo = "1"
	}

	return append(os.Environ(),
		"GOOS="+ctx.GOOS, "GOARCH="+ctx.GOARCH, "CGO_ENABLED="+cgo,
		"GOFLAGS=-trimpath", "GOPROXY=off", "GOTOOLCHAIN=local",
	)
}

// checkToolchain compares the go command each module's directory resolves
// with that module's go line, so the planner can tell a wrong image from code
// that doesn't compile. It returns the version by module directory.
func checkToolchain(ctx context.Context, root string, mods modules, o Options) (map[string]string, map[string]string, error) {
	versions := map[string]string{}
	tooOld := map[string]string{}

	for _, m := range mods {
		if !slices.Contains(o.Modules, m.Dir) {
			continue
		}

		have, err := GoVersion(ctx, filepath.Join(root, m.Dir), o.Build)
		if err != nil {
			return nil, nil, err
		}

		versions[m.Dir] = have

		b, err := os.ReadFile(filepath.Join(root, m.Dir, "go.mod")) //nolint:gosec // a go.mod of the tree
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %w", ErrModule, err)
		}

		f, err := modfile.ParseLax("go.mod", b, nil)
		if err != nil || f.Go == nil {
			continue // the go command reports a broken go.mod itself
		}

		if asks := "go" + f.Go.Version; version.IsValid(have) && version.Compare(have, asks) < 0 {
			tooOld[m.Dir] = fmt.Sprintf("%s asks for %s, the go command run there is %s", path.Join(m.Dir, "go.mod"), asks, have)
		}
	}

	// when no module can be read, the image is wrong, not the tree
	if len(tooOld) > 0 && len(tooOld) == len(versions) {
		for _, m := range o.Modules {
			if why := tooOld[m]; why != "" {
				return nil, nil, fmt.Errorf("%w: %s", ErrToolchain, why)
			}
		}
	}

	return versions, tooOld, nil
}

// BuildFlags are the flags that give the go command the build's tags.
func BuildFlags(b decls.Build) []string {
	ctx := b.Context()
	if len(ctx.BuildTags) == 0 {
		return nil
	}

	return []string{"-tags=" + strings.Join(ctx.BuildTags, ",")}
}

func newTree(root string, mods modules, o Options) *Tree {
	t := &Tree{
		Root: root, byID: map[goid.ID]int32{}, methods: map[int32][]int32{}, pkgs: map[string]*Package{},
		testFiles: map[string][]int32{}, mods: mods, pure: map[string]bool{}, requires: map[string][]string{},
		pureInputs: map[string]map[string]bool{}, misplaced: map[string]bool{},
		dirReplaced: map[string]map[string]bool{}, works: map[string]string{},
		syntax: map[*types.Package]bool{}, external: map[*types.Package]*external{}, build: o.Build,
		listed: map[string]listedPackage{}, unparsed: map[string]bool{}, testMethods: map[int32]bool{},
	}

	pure := o.PureCalls
	if pure == nil {
		pure = DefaultPureCalls
	}

	for _, f := range pure {
		t.pure[f] = true
	}

	t.purity = newPurity(t)

	return t
}

// Has tells whether the tree has a declaration.
func (t *Tree) Has(id goid.ID) bool {
	_, ok := t.byID[id]

	return ok
}

// Packages are the tree's package directories, sorted.
func (t *Tree) Packages() []string {
	return sortedKeys(t.pkgs)
}

// Package is one package directory, or nil.
func (t *Tree) Package(dir string) *Package {
	return t.pkgs[dir]
}

func (t *Tree) linkMethods() {
	for i, d := range t.Decls {
		if d.Recv == "" {
			continue
		}

		// a test method's type is a test declaration too, with its suffix,
		// unless the package itself declares it
		typ := goid.New(d.ID.Dir(), goid.Type, d.Recv)
		if _, suffix, ok := strings.Cut(string(d.ID), "@"); ok && !t.Has(typ) {
			typ += goid.ID("@" + suffix)
		}

		if ti, ok := t.byID[typ]; ok {
			t.methods[ti] = append(t.methods[ti], int32(i))
		}
	}
}

func (t *Tree) addPackage(p *packages.Package, moduleDir string, fset *token.FileSet, idx *goid.Index, seenFile map[string]bool) []work {
	if len(p.Syntax) == 0 || strings.HasSuffix(p.ID, ".test") {
		return nil
	}

	dir, ok := t.mods.dirOf(strings.TrimSuffix(p.PkgPath, "_test"))
	if !ok {
		return nil
	}

	if len(p.GoFiles) > 0 {
		dir = relPath(t.Root, filepath.Dir(p.GoFiles[0]))
	}

	pi := t.pkgs[dir]
	if pi == nil {
		pi = &Package{Dir: dir, Module: moduleDir}
		t.pkgs[dir] = pi
	}

	t.packageFacts(p, pi)

	if !strings.Contains(p.ID, " [") && p.Types != nil {
		t.typePkgs = append(t.typePkgs, p.Types)
	}

	if p.Types != nil {
		t.syntax[p.Types] = true
	}

	for _, f := range slices.Concat(p.GoFiles, p.OtherFiles, p.IgnoredFiles) {
		if r := relPath(t.Root, f); !slices.Contains(pi.Files, r) {
			pi.Files = append(pi.Files, r)
		}
	}

	// a package with a file that doesn't parse is its files: no
	// declaration of it is read, so none is named that go-sbom doesn't list
	if t.unparsed[dir] {
		return nil
	}

	var out []work

	for _, f := range p.Syntax {
		name := fset.Position(f.Package).Filename
		if seenFile[name] || !strings.HasPrefix(name, t.Root+string(filepath.Separator)) {
			continue
		}

		seenFile[name] = true

		out = append(out, t.addFile(p, f, pi, fset, idx))
	}

	return out
}

// DefaultPureCalls are the functions an initialiser may call and still be
// bound only by the code that uses its variable: they build a value and do
// nothing else.
var DefaultPureCalls = []string{
	"errors.New", "fmt.Errorf", "fmt.Sprintf", "fmt.Sprint",
	"regexp.MustCompile", "strings.NewReplacer", "time.Date",
}

// effectful tells whether initialiser expressions may act beyond giving the
// variable its value: a call that is no conversion, no builtin and not in
// pure.
func effectful(exprs []ast.Expr, info *types.Info, pure func(*types.Func) bool) bool {
	found := false

	for _, e := range exprs {
		ast.Inspect(e, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || found {
				return !found
			}

			if tv, ok := info.Types[call.Fun]; ok && tv.IsType() {
				return true
			}

			var obj types.Object

			switch fun := ast.Unparen(call.Fun).(type) {
			case *ast.Ident:
				obj = info.Uses[fun]
			case *ast.SelectorExpr:
				obj = info.Uses[fun.Sel]
			}

			switch o := obj.(type) {
			case *types.Builtin:
				return true
			case *types.Func:
				if pure != nil && o.Pkg() != nil && o.Signature().Recv() == nil && pure(o) {
					return true
				}
			}

			found = true

			return false
		})
	}

	return found
}

// initActs tells whether a variable's initialiser may act beyond giving it its
// value when its package starts: a call outside pure, or an expression that
// can panic or block.
func initActs(spec *ast.ValueSpec, info *types.Info, pure func(*types.Func) bool) bool {
	if effectful(spec.Values, info, pure) {
		return true
	}

	commaOK := len(spec.Names) == 2 && len(spec.Values) == 1

	for _, v := range spec.Values {
		if mayPanic(v, info, commaOK) {
			return true
		}
	}

	return false
}

func (t *Tree) listedPure(fn *types.Func) bool {
	return t.pure[fn.Pkg().Path()+"."+fn.Name()]
}

// resolveInits decides, once every module is read, which variables whose
// initialisers call outside the pure calls call only functions of the tree
// proved pure: they don't act. The files a proof read become inputs of
// the variable's package, since its answer depends on them.
func (t *Tree) resolveInits() {
	for _, v := range t.pending {
		read := map[string]bool{}

		v.decl.Init = initActs(v.spec, v.info, func(fn *types.Func) bool {
			if t.listedPure(fn) {
				return true
			}

			if fn.Origin().Signature().TypeParams().Len() > 0 {
				return false
			}

			return t.purity.call(fn.Pkg().Path(), fn.Name(), read)
		})

		if t.pureInputs[v.dir] == nil {
			t.pureInputs[v.dir] = map[string]bool{}
		}

		for f := range read {
			t.pureInputs[v.dir][f] = true
		}
	}

	t.pending = nil
}

func (t *Tree) addFile(p *packages.Package, f *ast.File, pi *Package, fset *token.FileSet, idx *goid.Index) work {
	path := fset.Position(f.Package).Filename
	base := filepath.Base(path)
	test := strings.HasSuffix(base, "_test.go")
	ds := goid.File(pi.Dir, base, f)
	idx.Add(fset, ds)

	if test {
		pi.TestPulls = append(pi.TestPulls, pullsOf(f)...)
	} else {
		pi.Pulls = append(pi.Pulls, pullsOf(f)...)
	}

	switch why := wholeReason(f, p.TypesInfo); {
	case why == "":
	case !test && pi.Whole == "":
		pi.Whole = why
	case test && strings.HasPrefix(why, "reflection by name"):
		// a test file's reflection by name follows the per-site rule while
		// interface-methods is on
		if pi.TestWholeOff == "" {
			pi.TestWholeOff = why
		}
	case test && pi.TestWhole == "":
		pi.TestWhole = why
	}

	out := make([]*Decl, 0, len(ds))

	for _, gd := range ds {
		d := &Decl{ID: gd.ID, File: relPath(t.Root, path), Test: test, Recv: gd.Recv, node: gd.Node}

		switch n := gd.Node.(type) {
		case *ast.FuncDecl:
			d.Empty = n.Body == nil || len(n.Body.List) == 0
			d.Init = gd.ID.Kind() == goid.Init
			d.InitList, d.InitAny = d.Init, d.Init
		case *ast.ValueSpec:
			if gd.ID.Kind() == goid.Const {
				// a spec without values repeats an earlier one's: it
				// references what the group does
				d.node = gd.Gen
			} else {
				d.InitList = initActs(n, p.TypesInfo, t.listedPure)
				d.InitAny = initActs(n, p.TypesInfo, nil)
				// what the list doesn't know, the tree's functions may
				// prove pure once every module is read
				d.Init = d.InitList

				if d.InitList {
					t.pending = append(t.pending, pendingVar{decl: d, dir: pi.Dir, spec: n, info: p.TypesInfo})
				}

				d.Valued = test && len(n.Values) > 0
			}
		}

		i := int32(len(t.Decls)) //nolint:gosec // fits
		t.Decls = append(t.Decls, d)
		t.byID[d.ID] = i

		if test {
			t.testFiles[d.File] = append(t.testFiles[d.File], i)
			pi.TestDecls = append(pi.TestDecls, i)
		} else {
			pi.Decls = append(pi.Decls, i)
		}

		out = append(out, d)
	}

	return work{info: p.TypesInfo, decls: out, file: f, test: test}
}

func (t *Tree) packageFacts(p *packages.Package, pi *Package) {
	test := strings.Contains(p.ID, " [") || strings.HasSuffix(p.Name, "_test")

	for ip := range p.Imports {
		d, ok := t.mods.dirOf(ip)
		if !ok || d == pi.Dir {
			continue
		}

		if test {
			if !slices.Contains(pi.TestImports, d) {
				pi.TestImports = append(pi.TestImports, d)
			}
		} else if !slices.Contains(pi.Imports, d) {
			pi.Imports = append(pi.Imports, d)
		}
	}

	if test {
		return
	}

	pi.Name = p.Name

	if len(p.OtherFiles) > 0 && pi.Whole == "" {
		pi.Whole = "non-Go files (" + filepath.Base(p.OtherFiles[0]) + ")"
	}
}

// excluded notes the units of the files each package's build leaves out, one
// per file by its content. No recording sees their declarations, so each
// binds every slice linking its package.
func (t *Tree) excluded(o Options) error {
	ctx := o.Build.Context()

	for dir, p := range t.pkgs {
		entries, err := os.ReadDir(filepath.Join(t.Root, dir))
		if err != nil {
			return fmt.Errorf("%w: %w", ErrLoad, err)
		}

		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
				continue
			}

			if ok, err := ctx.MatchFile(filepath.Join(t.Root, dir), e.Name()); err != nil || !ok {
				p.Excluded = append(p.Excluded, goid.New(dir, goid.Excluded, e.Name()))
			}
		}
	}

	return nil
}

// wholeReason says why a file's package can't be bound by declaration: what
// the analysis can't follow.
func wholeReason(f *ast.File, info *types.Info) string {
	for _, im := range f.Imports {
		if im.Path.Value == `"C"` {
			return "cgo"
		}
	}

	for _, g := range f.Comments {
		for _, c := range g.List {
			if strings.HasPrefix(c.Text, "//go:linkname") {
				return "go:linkname"
			}
		}
	}

	why := ""

	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncDecl:
			if n.Body == nil {
				why = "a function without a body (" + n.Name.Name + ")"
			}
		case *ast.SelectorExpr:
			switch n.Sel.Name {
			case methodByName, "FieldByName", "FieldByNameFunc":
				if o := info.Uses[n.Sel]; o != nil && o.Pkg() != nil && o.Pkg().Path() == reflectPkg {
					why = "reflection by name (" + n.Sel.Name + ")"
				}
			}
		}

		return why == ""
	})

	return why
}

func relPath(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return filepath.ToSlash(p)
	}

	return filepath.ToSlash(r)
}

func sortedKeys[K interface{ ~string | ~int32 }, V any](m map[K]V) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	slices.Sort(out)

	return out
}
