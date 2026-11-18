package graph

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"

	"github.com/sideways-works/pipelinegen-sbom-go/goid"
)

// external is a package of the tree the type checker read from export data:
// its declarations as go-sbom names them, read from its files without a type
// check.
type external struct {
	// byObject maps the package's objects to their declarations: package
	// level ones by name, a struct field or an interface method by the
	// declaration it sits in.
	byObject map[types.Object]int32
	// byName maps the package's top-level names to their declarations; a
	// constant or a variable is named by its spec, which may hold several.
	byName map[string]int32
	// named are the type names declared as another named type (type R
	// Order, type A = Order): they share its fields, which belong to the
	// declaration whose own type literal holds them.
	named map[string]bool
}

func (t *Tree) externalID(o types.Object) (int32, bool) {
	switch v := o.(type) {
	case *types.Func:
		o = v.Origin()
	case *types.Var:
		o = v.Origin()
	}

	pkg := o.Pkg()

	e := t.external[pkg]
	if e == nil {
		dir, ok := t.mods.dirOf(pkg.Path())
		if !ok {
			return 0, false
		}

		if p := t.pkgs[dir]; p != nil && len(p.Decls) > 0 {
			// a package of the tree another module's load read from export
			// data, while its own module loaded it from source: its
			// declarations are already the tree's
			e = t.loadedExternal(p, pkg)
		} else {
			e = t.readExternal(dir, pkg)
		}

		t.external[pkg] = e
	}

	if i, ok := e.byObject[o]; ok {
		return i, true
	}

	if fn, ok := o.(*types.Func); ok {
		if recv := fn.Signature().Recv(); recv != nil {
			if named := namedOf(recv.Type()); named != nil && !types.IsInterface(named) {
				i, ok := e.byName["method:"+named.Origin().Obj().Name()+"."+fn.Name()]

				return i, ok
			}
		}
	}

	return 0, false
}

func namedOf(tt types.Type) *types.Named {
	if p, ok := tt.(*types.Pointer); ok {
		tt = p.Elem()
	}

	if n, ok := types.Unalias(tt).(*types.Named); ok {
		return n
	}

	return nil
}

// readExternal reads a package of the tree from its files: its
// declarations join the tree (outside its packages, which are the ones
// loaded from source), and its objects are mapped to them.
func (t *Tree) readExternal(dir string, pkg *types.Package) *external {
	e := &external{byObject: map[types.Object]int32{}, byName: map[string]int32{}, named: map[string]bool{}}
	abs := filepath.Join(t.Root, dir)
	ctx := t.build.Context()
	fset := token.NewFileSet()

	entries, err := os.ReadDir(abs)
	if err != nil {
		return e
	}

	first := int32(len(t.Decls)) //nolint:gosec // fits

	defer t.externalMethods(first)

	var testRecvs []string

	for _, en := range entries {
		name := en.Name()
		if en.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}

		if ok, err := ctx.MatchFile(abs, name); err != nil || !ok {
			continue
		}

		f, err := parser.ParseFile(fset, filepath.Join(abs, name), nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}

		if strings.HasSuffix(name, "_test.go") {
			testRecvs = append(testRecvs, testReceivers(f)...)

			continue
		}

		t.externalFile(e, dir, name, f)
	}

	defer func() {
		for _, r := range testRecvs {
			if ti, ok := t.byID[goid.New(dir, goid.Type, r)]; ok && ti >= first {
				t.testMethods[ti] = true
			}
		}
	}()

	e.mapObjects(pkg)

	return e
}

func (t *Tree) loadedExternal(p *Package, pkg *types.Package) *external {
	e := &external{byObject: map[types.Object]int32{}, byName: map[string]int32{}, named: map[string]bool{}}

	for _, i := range p.Decls {
		_, kind, n, _ := goid.Parse(string(t.Decls[i].ID)) //nolint:errcheck // goid made it

		switch kind {
		case goid.Const, goid.Var:
			for one := range strings.SplitSeq(n, ",") {
				e.byName[one] = i
			}
		case goid.Type, goid.Func:
			e.byName[n] = i

			if ts, ok := t.Decls[i].node.(*ast.TypeSpec); ok && namesType(ts.Type) {
				e.named[n] = true
			}
		case goid.Method:
			e.byName["method:"+n] = i
		case goid.Init, goid.Excluded, goid.TypeDecl, goid.MethodSig, goid.API:
		}
	}

	e.mapObjects(pkg)

	return e
}

// testReceivers are the receivers of the methods a test file of the
// package itself declares: such a method gives its type a declaration of
// its own, as go-sbom lists it.
func testReceivers(f *ast.File) []string {
	if strings.HasSuffix(f.Name.Name, "_test") {
		return nil
	}

	var out []string

	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv != nil && len(fd.Recv.List) > 0 {
			out = append(out, goid.RecvName(fd.Recv.List[0].Type))
		}
	}

	return out
}

// externalMethods notes the methods of the types of a package read from
// export data, its declarations from first on: whether a type has methods
// decides whether a reference binds its declaration alone.
func (t *Tree) externalMethods(first int32) {
	for i := first; int(i) < len(t.Decls); i++ {
		d := t.Decls[i]
		if d.Recv == "" {
			continue
		}

		if ti, ok := t.byID[goid.New(d.ID.Dir(), goid.Type, d.Recv)]; ok {
			t.methods[ti] = append(t.methods[ti], i)
		}
	}
}

func (t *Tree) externalFile(e *external, dir, name string, f *ast.File) {
	for _, gd := range goid.File(dir, name, f) {
		d := &Decl{ID: gd.ID, File: filepath.ToSlash(filepath.Join(dir, name)), Recv: gd.Recv}

		if fd, ok := gd.Node.(*ast.FuncDecl); ok {
			d.Empty = fd.Body == nil || len(fd.Body.List) == 0
		}

		i := int32(len(t.Decls)) //nolint:gosec // fits
		t.Decls = append(t.Decls, d)
		t.byID[d.ID] = i

		_, kind, n, _ := goid.Parse(string(gd.ID)) //nolint:errcheck // goid made it

		switch kind {
		case goid.Const, goid.Var:
			for one := range strings.SplitSeq(n, ",") {
				e.byName[one] = i
			}
		case goid.Type, goid.Func:
			e.byName[n] = i

			if ts, ok := gd.Node.(*ast.TypeSpec); ok && namesType(ts.Type) {
				e.named[n] = true
			}
		case goid.Method:
			e.byName["method:"+n] = i
		case goid.Init, goid.Excluded, goid.TypeDecl, goid.MethodSig, goid.API:
		}
	}
}

func (e *external) mapObjects(pkg *types.Package) {
	sc := pkg.Scope()
	for _, n := range sc.Names() {
		o := sc.Lookup(n)

		i, ok := e.byName[n]
		if !ok {
			continue
		}

		e.byObject[o] = i

		// fields and interface methods belong to the declaration they sit in
		tt := o.Type()
		if _, isType := o.(*types.TypeName); isType {
			if e.named[n] {
				continue
			}

			tt = tt.Underlying()
		}

		inner(tt, map[types.Type]bool{}, func(x types.Object) { e.byObject[x] = i })
	}
}

// inner calls fn for the fields and interface methods of a type, inside
// the type literals it is made of, without entering named types: those
// are declarations of their own.
func inner(tt types.Type, seen map[types.Type]bool, fn func(types.Object)) {
	if tt == nil || seen[tt] {
		return
	}

	seen[tt] = true

	switch tt := tt.(type) {
	case *types.Struct:
		for f := range tt.Fields() {
			fn(f)
			inner(f.Type(), seen, fn)
		}
	case *types.Interface:
		for m := range tt.ExplicitMethods() {
			fn(m)
			inner(m.Type(), seen, fn)
		}

		for e := range tt.EmbeddedTypes() {
			inner(e, seen, fn)
		}
	case *types.Pointer:
		inner(tt.Elem(), seen, fn)
	case *types.Slice:
		inner(tt.Elem(), seen, fn)
	case *types.Array:
		inner(tt.Elem(), seen, fn)
	case *types.Chan:
		inner(tt.Elem(), seen, fn)
	case *types.Map:
		inner(tt.Key(), seen, fn)
		inner(tt.Elem(), seen, fn)
	case *types.Signature:
		inner(tt.Params(), seen, fn)
		inner(tt.Results(), seen, fn)
	case *types.Tuple:
		for v := range tt.Variables() {
			inner(v.Type(), seen, fn)
		}
	}
}

func namesType(x ast.Expr) bool {
	switch x := x.(type) {
	case *ast.Ident, *ast.SelectorExpr, *ast.IndexExpr, *ast.IndexListExpr:
		return true
	case *ast.ParenExpr:
		return namesType(x.X)
	}

	return false
}
