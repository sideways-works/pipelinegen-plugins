package graph

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sideways-works/pipelinegen-plugins/sbom/go/goid"
)

// purity proves functions of the tree pure from their syntax alone, so the
// answer is the same from source or export data: a plain function whose body
// is one return of values built from literals, names, composite literals,
// arithmetic that can't panic and calls of pure functions.
type purity struct {
	t    *Tree
	pkgs map[string]*synPackage
	// state is, by function key, whether it is pure, while known
	state map[string]pureState
	// reads are, by function key, the units its proof read, through the
	// functions it calls: a proof served from state adds them all, so a
	// variable's input units are the same whichever package asked first
	reads map[string]map[string]bool
}

type pureState int8

const (
	pureVisiting pureState = iota + 1
	pureYes
	pureNo
)

// synPackage is a package of the tree as its files' syntax says it, under
// the tree's build context, without its tests.
type synPackage struct {
	dir, path string
	name      string
	files     []string
	funcs     map[string]*synFunc
	// kinds are the package's type names by what they declare: a struct,
	// an array or slice, or anything else; typeFiles the files that
	// declare them
	kinds     map[string]litKind
	typeFiles map[string]string
	// ids are the package's top-level names as go-sbom names their
	// declarations
	ids map[string]goid.ID
}

type synFunc struct {
	decl    *ast.FuncDecl
	file    string            // relative to the tree's root
	imports map[string]string // name in the file → import path
	// unfollowed says its file holds what the analysis can't follow: cgo
	// or a go:linkname directive
	unfollowed bool
}

// litKind is what a composite literal of a named type builds.
type litKind int8

const (
	litOther litKind = iota
	litStruct
	litList
)

func newPurity(t *Tree) *purity {
	return &purity{t: t, pkgs: map[string]*synPackage{}, state: map[string]pureState{}, reads: map[string]map[string]bool{}}
}

// call tells whether a call of importPath.name only builds its result, and
// adds the files the proof read to read. They are inputs of the variable's
// set, so the planner asks again when one changes.
func (p *purity) call(importPath, name string, read map[string]bool) bool {
	if p.t.pure[importPath+"."+name] {
		return true
	}

	pkg := p.pkg(importPath)
	if pkg == nil || p.t.misplaced[importPath] {
		return false
	}

	key := importPath + "." + name
	fn := pkg.funcs[name]

	if p.state[key] == pureVisiting {
		// a cycle proves nothing; the proof that started it reads the file
		return false
	}

	if p.state[key] == 0 {
		// the module's go.mod too: its go line decides which files a
		// version's build tag keeps
		own := map[string]bool{}
		if md, ok := p.t.mods.moduleOf(pkg.dir); ok {
			own[path.Join(md, "go.mod")] = true
		}

		if fn != nil {
			own[fn.file] = true
		} else {
			for _, f := range pkg.files {
				own[f] = true
			}
		}

		p.state[key] = pureVisiting
		ok := fn != nil && !fn.unfollowed && p.body(pkg, fn, own)

		p.state[key], p.reads[key] = pureNo, own
		if ok {
			p.state[key] = pureYes
		}
	}

	for u := range p.reads[key] {
		read[u] = true
	}

	return p.state[key] == pureYes
}

func (p *purity) body(pkg *synPackage, fn *synFunc, read map[string]bool) bool {
	d := fn.decl
	if d.Recv != nil || d.Type.TypeParams != nil || d.Body == nil || len(d.Body.List) != 1 {
		return false
	}

	ret, ok := d.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) == 0 {
		return false
	}

	e := exprCheck{p: p, pkg: pkg, fn: fn, read: read, params: map[string]bool{}}

	for _, fl := range []*ast.FieldList{d.Type.Params, d.Type.Results} {
		if fl == nil {
			continue
		}

		for _, f := range fl.List {
			for _, n := range f.Names {
				e.params[n.Name] = true
			}
		}
	}

	for _, r := range ret.Results {
		if !e.pure(r) {
			return false
		}
	}

	return true
}

type exprCheck struct {
	p      *purity
	pkg    *synPackage
	fn     *synFunc
	read   map[string]bool
	params map[string]bool
}

// pure tells whether evaluating an expression can't act, panic or block.
func (e exprCheck) pure(x ast.Expr) bool {
	switch n := x.(type) {
	case *ast.BasicLit, *ast.Ident:
		// a literal, or reading a name: a parameter, a constant, a
		// variable, a function value
		return true
	case *ast.ParenExpr:
		return e.pure(n.X)
	case *ast.SelectorExpr:
		// a name of another package; a field or method of a value may go
		// through a nil pointer or interface
		_, ok := e.qualified(n)

		return ok
	case *ast.CompositeLit:
		return e.literal(n, e.kindOf(n.Type))
	case *ast.UnaryExpr:
		switch n.Op {
		case token.AND:
			lit, ok := ast.Unparen(n.X).(*ast.CompositeLit)

			return ok && e.pure(lit)
		case token.ADD, token.SUB, token.NOT, token.XOR:
			return e.pure(n.X)
		default:
			return false
		}
	case *ast.BinaryExpr:
		switch n.Op {
		case token.ADD, token.SUB, token.MUL, token.AND, token.OR, token.XOR, token.AND_NOT, token.LAND, token.LOR:
			return e.pure(n.X) && e.pure(n.Y)
		default:
			// division, remainder and shifts can panic; comparing
			// interface values can too
			return false
		}
	case *ast.CallExpr:
		return e.call(n)
	}

	return false
}

// call: a call of a pure function of this package or of an imported one,
// with pure arguments. A conversion or a builtin is no function the
// package declares, so it counts as acting.
func (e exprCheck) call(c *ast.CallExpr) bool {
	var importPath, name string

	switch fun := ast.Unparen(c.Fun).(type) {
	case *ast.Ident:
		if e.params[fun.Name] {
			return false
		}

		importPath, name = e.pkg.path, fun.Name
	case *ast.SelectorExpr:
		p, ok := e.qualified(fun)
		if !ok {
			return false
		}

		importPath, name = p, fun.Sel.Name
	default:
		return false
	}

	for _, a := range c.Args {
		if !e.pure(a) {
			return false
		}
	}

	return e.p.call(importPath, name, e.read)
}

// qualified is the import path of pkg in pkg.Name, when pkg names an
// import of the function's file and no parameter.
func (e exprCheck) qualified(s *ast.SelectorExpr) (string, bool) {
	id, ok := s.X.(*ast.Ident)
	if !ok || e.params[id.Name] {
		return "", false
	}

	p, ok := e.fn.imports[id.Name]

	return p, ok
}

// kindOf is what a composite literal's type builds, as far as the syntax
// tells: a struct or a list type written out or named in a package of the
// tree; anything else, a map above all, is other.
func (e exprCheck) kindOf(t ast.Expr) litKind {
	switch n := ast.Unparen(t).(type) {
	case *ast.StructType:
		return litStruct
	case *ast.ArrayType:
		return litList
	case *ast.Ident:
		if f, ok := e.pkg.typeFiles[n.Name]; ok {
			e.read[f] = true
		}

		return e.pkg.kinds[n.Name]
	case *ast.SelectorExpr:
		p, ok := e.qualified(n)
		if !ok {
			return litOther
		}

		pkg := e.p.pkg(p)
		if pkg == nil {
			return litOther
		}

		if f, ok := pkg.typeFiles[n.Sel.Name]; ok {
			e.read[f] = true
		}

		return pkg.kinds[n.Sel.Name]
	}

	return litOther
}

// literal: a composite literal of pure values. A struct's keys are field
// names and a list's are constants, both checked by the compiler; any
// other key (a map's) must be a literal, since hashing a computed one may
// panic.
func (e exprCheck) literal(lit *ast.CompositeLit, kind litKind) bool {
	for _, el := range lit.Elts {
		v := el

		if kv, ok := el.(*ast.KeyValueExpr); ok {
			v = kv.Value

			if _, basic := kv.Key.(*ast.BasicLit); !basic && kind == litOther {
				return false
			}
		}

		if inner, ok := v.(*ast.CompositeLit); ok && inner.Type == nil {
			// an elided type: its own kind is unknown here
			if !e.literal(inner, litOther) {
				return false
			}

			continue
		}

		if !e.pure(v) {
			return false
		}
	}

	return true
}

func (p *purity) pkg(importPath string) *synPackage {
	dir, ok := p.t.mods.dirOf(importPath)
	if !ok {
		return nil
	}

	if s, ok := p.pkgs[dir]; ok {
		return s
	}

	s := p.t.readSyntax(dir, importPath)
	p.pkgs[dir] = s

	return s
}

func (t *Tree) readSyntax(dir, importPath string) *synPackage {
	s := &synPackage{
		dir: dir, path: importPath, funcs: map[string]*synFunc{}, kinds: map[string]litKind{}, typeFiles: map[string]string{},
		ids: map[string]goid.ID{},
	}
	abs := filepath.Join(t.Root, dir)
	ctx := t.build.Context()
	fset := token.NewFileSet()

	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil
	}

	for _, en := range entries {
		name := en.Name()
		if en.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		if ok, err := ctx.MatchFile(abs, name); err != nil || !ok {
			continue
		}

		s.files = append(s.files, path.Join(dir, name))

		f, err := parser.ParseFile(fset, filepath.Join(abs, name), nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			continue
		}

		s.addFile(dir, name, f)
	}

	return s
}

func (s *synPackage) addFile(dir, name string, f *ast.File) {
	s.name = f.Name.Name
	imports, cgo := importsOf(f)
	unfollowed := cgo || hasLinkname(f)

	for _, d := range goid.File(dir, name, f) {
		_, _, n, _ := goid.Parse(string(d.ID)) //nolint:errcheck // goid made it
		for one := range strings.SplitSeq(n, ",") {
			if _, taken := s.ids[one]; !taken {
				s.ids[one] = d.ID
			}
		}
	}

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil && d.Name.Name != "init" && d.Name.Name != "_" {
				s.funcs[d.Name.Name] = &synFunc{decl: d, file: path.Join(dir, name), imports: imports, unfollowed: unfollowed}
			}
		case *ast.GenDecl:
			s.types(d, path.Join(dir, name))
		}
	}
}

func importsOf(f *ast.File) (map[string]string, bool) {
	out := map[string]string{}
	cgo := false

	for _, im := range f.Imports {
		p, err := strconv.Unquote(im.Path.Value)
		if err != nil {
			continue
		}

		if p == "C" {
			cgo = true
		}

		n := guessName(p)
		if im.Name != nil {
			n = im.Name.Name
		}

		if n != "_" && n != "." {
			out[n] = p
		}
	}

	return out, cgo
}

func (s *synPackage) types(d *ast.GenDecl, file string) {
	for _, sp := range d.Specs {
		ts, ok := sp.(*ast.TypeSpec)
		if !ok {
			continue
		}

		s.typeFiles[ts.Name.Name] = file

		switch ts.Type.(type) {
		case *ast.StructType:
			s.kinds[ts.Name.Name] = litStruct
		case *ast.ArrayType:
			s.kinds[ts.Name.Name] = litList
		}
	}
}

// guessName is the name an import without an alias goes by, as its path
// suggests: the last element, without a major version or a gopkg.in
// suffix. A wrong guess only finds no function, which counts as acting.
func guessName(importPath string) string {
	elems := strings.Split(importPath, "/")
	last := elems[len(elems)-1]

	if len(elems) > 1 && len(last) > 1 && last[0] == 'v' && strings.Trim(last[1:], "0123456789") == "" {
		last = elems[len(elems)-2]
	}

	if i := strings.Index(last, ".v"); i > 0 {
		last = last[:i]
	}

	return strings.ReplaceAll(last, "-", "_")
}

func hasLinkname(f *ast.File) bool {
	for _, g := range f.Comments {
		for _, c := range g.List {
			if strings.HasPrefix(c.Text, "//go:linkname") {
				return true
			}
		}
	}

	return false
}

// pullsOf are the declarations a file's go:linkname directives name in
// another package, as import/path.Name: a pull that reads or calls them
// without a reference the type checker sees.
func pullsOf(f *ast.File) []string {
	var out []string

	for _, g := range f.Comments {
		for _, c := range g.List {
			rest, ok := strings.CutPrefix(c.Text, "//go:linkname ")
			if !ok {
				continue
			}

			fields := strings.Fields(rest)
			if len(fields) != 2 {
				continue
			}

			if i := strings.LastIndex(fields[1], "."); i > 0 && !strings.ContainsAny(fields[1][i+1:], "()*") {
				out = append(out, fields[1])
			}
		}
	}

	return out
}

func (p *purity) pullTarget(target string) (goid.ID, bool) {
	i := strings.LastIndex(target, ".")
	pkg := p.pkg(target[:i])

	if pkg == nil {
		return "", false
	}

	id, ok := pkg.ids[target[i+1:]]

	return id, ok
}
