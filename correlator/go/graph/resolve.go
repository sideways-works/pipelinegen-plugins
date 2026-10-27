package graph

import (
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sideways-works/pipelinegen-sbom-go/goid"
)

// A value whose static type is an interface, handed over, can hold any type
// that implements it unless the code around it says which. A variable's
// concrete types are read from every use of it in its function, or its
// package for an unexported package variable; any use that could write it
// unseen makes it unknown, and it then binds as its static type does.

// holes tells whether a value of type tt holds interface values the
// reading can follow: an interface, or an unnamed slice, array or map of
// what holds them.
func holes(tt types.Type) bool {
	switch tt := types.Unalias(tt).(type) {
	case *types.Interface:
		return true
	case *types.Named:
		return types.IsInterface(tt)
	case *types.Slice:
		return holes(tt.Elem())
	case *types.Array:
		return holes(tt.Elem())
	case *types.Map:
		return holes(tt.Key()) || holes(tt.Elem())
	}

	return false
}

// shares tells whether a value of type tt shares what it holds with its
// copies: a slice or a map does (an array's copy is its own, but a slice
// of it is not, so it counts too).
func shares(tt types.Type) bool {
	switch types.Unalias(tt).Underlying().(type) {
	case *types.Slice, *types.Map, *types.Array:
		return holes(tt) || holes(types.Unalias(tt).Underlying())
	}

	return false
}

type resolver struct {
	t     *Tree
	fset  *token.FileSet
	idx   *goid.Index
	byDir map[string][]work
	files map[*ast.File]*fileIndex
	dir   string
}

type fileIndex struct {
	w      work
	parent map[ast.Node]ast.Node
	uses   map[types.Object][]*ast.Ident
	defs   map[types.Object]*ast.Ident
	// pkgUses and pkgDefs are the same for the package's own variables,
	// by name: a test file sees another copy of the package's objects.
	pkgUses map[string][]*ast.Ident
	pkgDefs map[string]*ast.Ident
}

func newResolver(t *Tree, fset *token.FileSet, idx *goid.Index, todo []work) *resolver {
	r := &resolver{t: t, fset: fset, idx: idx, byDir: map[string][]work{}, files: map[*ast.File]*fileIndex{}}

	for _, w := range todo {
		dir := relPath(t.Root, filepath.Dir(fset.Position(w.file.Package).Filename))
		r.byDir[dir] = append(r.byDir[dir], w)
	}

	return r
}

// enter keeps the files read for one directory only: a variable is read
// in its function's file or its package's files, and the files of a
// package come together.
func (r *resolver) enter(dir string) {
	if dir != r.dir {
		r.dir = dir
		r.files = map[*ast.File]*fileIndex{}
	}
}

func (r *resolver) index(w work) *fileIndex {
	if fi, ok := r.files[w.file]; ok {
		return fi
	}

	fi := &fileIndex{
		w: w, parent: map[ast.Node]ast.Node{}, uses: map[types.Object][]*ast.Ident{}, defs: map[types.Object]*ast.Ident{},
		pkgUses: map[string][]*ast.Ident{}, pkgDefs: map[string]*ast.Ident{},
	}

	var stack []ast.Node

	ast.Inspect(w.file, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]

			return true
		}

		if len(stack) > 0 {
			fi.parent[n] = stack[len(stack)-1]
		}

		stack = append(stack, n)

		if id, ok := n.(*ast.Ident); ok {
			fi.ident(id)
		}

		return true
	})

	r.files[w.file] = fi

	return fi
}

func (fi *fileIndex) ident(id *ast.Ident) {
	if o := fi.w.info.Uses[id]; o != nil {
		fi.uses[o] = append(fi.uses[o], id)

		if packageVar(o) {
			fi.pkgUses[o.Name()] = append(fi.pkgUses[o.Name()], id)
		}
	}

	if o := fi.w.info.Defs[id]; o != nil {
		fi.defs[o] = id

		if packageVar(o) {
			fi.pkgDefs[o.Name()] = id
		}
	}
}

func packageVar(o types.Object) bool {
	v, ok := o.(*types.Var)

	return ok && v.Pkg() != nil && v.Parent() == v.Pkg().Scope()
}

// resolution is one value's reading: the concrete types found, the
// variables whose writes count, and the first reason it can't be told.
type resolution struct {
	r    *resolver
	site ast.Expr
	out  []types.Type
	// via are the methods whose parameter the value is, handed on as it
	// is: it holds what their calls pass in
	via  []forward
	seen map[string]bool
	why  string
}

// forward is a method that hands one of its parameters on as it is.
type forward struct {
	// node is the junction of what its calls pass to it
	node, name string
	test       bool
}

// resolve tells the concrete types the interface parts of x, an argument
// handed over in w, can hold, and the methods whose parameter it is, or
// why that can't be told.
func (r *resolver) resolve(w work, x ast.Expr) ([]types.Type, []forward, string) {
	res := &resolution{r: r, site: ast.Unparen(x), seen: map[string]bool{}}

	if !res.collect(r.index(w), x) {
		return nil, nil, res.why
	}

	return res.out, res.via, ""
}

// results tells the concrete types the interface parts of a method's i-th
// result can hold: what every return statement of its body puts there.
func (r *resolver) results(dir string, m *types.Func, i int) ([]types.Type, string) {
	w, fd := r.funcDecl(dir, m)
	if fd == nil || fd.Body == nil {
		return nil, "a method whose body the reading can't follow"
	}

	fi := r.index(w)
	res := &resolution{r: r, seen: map[string]bool{}}
	n := m.Signature().Results().Len()
	ok := true

	ast.Inspect(fd.Body, func(x ast.Node) bool {
		if !ok {
			return false
		}

		switch x := x.(type) {
		case *ast.FuncLit:
			return false // its returns are its own
		case *ast.ReturnStmt:
			if len(x.Results) != n {
				ok = res.fail(fi, x, "a return the reading can't follow")

				return false
			}

			res.site = ast.Unparen(x.Results[i])
			ok = res.collect(fi, x.Results[i])
		}

		return true
	})

	if !ok {
		return nil, res.why
	}

	return res.out, ""
}

func (r *resolver) funcDecl(dir string, m *types.Func) (work, *ast.FuncDecl) {
	for _, w := range r.byDir[dir] {
		if m.Pos() < w.file.FileStart || m.Pos() >= w.file.FileEnd {
			continue
		}

		for _, d := range w.file.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Pos() == m.Pos() {
				return w, fd
			}
		}
	}

	return work{}, nil
}

func (res *resolution) fail(_ *fileIndex, at ast.Node, why string) bool {
	if res.why == "" {
		p := res.r.fset.Position(at.Pos())
		res.why = why + " at " + filepath.Base(p.Filename) + ":" + strconv.Itoa(p.Line)
	}

	return false
}

func (res *resolution) add(tt types.Type) {
	for _, o := range res.out {
		if types.Identical(o, tt) {
			return
		}
	}

	res.out = append(res.out, tt)
}

// collect adds what the interface parts of e's value can be: e's own
// type when it has none, else what each part was made of.
func (res *resolution) collect(fi *fileIndex, e ast.Expr) bool {
	e = ast.Unparen(e)
	info := fi.w.info

	tv, ok := info.Types[e]
	if !ok || tv.Type == nil {
		return res.fail(fi, e, "a value whose type can't be told")
	}

	if tv.IsNil() {
		return true
	}

	if !holes(tv.Type) {
		res.add(tv.Type)

		return true
	}

	switch e := e.(type) {
	case *ast.CompositeLit:
		return res.composite(fi, e, tv.Type)
	case *ast.Ident:
		v, ok := info.Uses[e].(*types.Var)
		if !ok {
			return res.fail(fi, e, "a value that is no variable")
		}

		return res.variable(fi, v, e)
	case *ast.CallExpr:
		return res.call(fi, e)
	case *ast.IndexExpr:
		if !container(info.TypeOf(e.X)) {
			return res.fail(fi, e, "an element of what the reading can't follow")
		}

		return res.collect(fi, e.X)
	case *ast.SliceExpr:
		return res.collect(fi, e.X)
	case *ast.TypeAssertExpr:
		return res.collect(fi, e.X)
	case *ast.SelectorExpr:
		if v, ok := info.Uses[e.Sel].(*types.Var); ok && !v.IsField() {
			return res.fail(fi, e, "a variable of another package")
		}

		return res.fail(fi, e, "a field")
	case *ast.UnaryExpr:
		if e.Op == token.ARROW {
			return res.fail(fi, e, "a value received from a channel")
		}
	case *ast.StarExpr:
		return res.fail(fi, e, "a value read through a pointer")
	}

	return res.fail(fi, e, "a value the reading can't follow")
}

func (res *resolution) composite(fi *fileIndex, c *ast.CompositeLit, tt types.Type) bool {
	_, isMap := types.Unalias(tt).Underlying().(*types.Map)

	for _, el := range c.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			if !res.collect(fi, el) {
				return false
			}

			continue
		}

		if isMap && !res.collect(fi, kv.Key) {
			return false
		}

		if !res.collect(fi, kv.Value) {
			return false
		}
	}

	return true
}

func container(tt types.Type) bool {
	if tt == nil {
		return false
	}

	switch u := types.Unalias(tt).Underlying().(type) {
	case *types.Slice, *types.Array, *types.Map:
		return true
	case *types.Pointer:
		_, ok := u.Elem().Underlying().(*types.Array)

		return ok
	}

	return false
}

func (res *resolution) call(fi *fileIndex, c *ast.CallExpr) bool {
	info := fi.w.info

	if tv, ok := info.Types[c.Fun]; ok && tv.IsType() && len(c.Args) == 1 {
		return res.collect(fi, c.Args[0])
	}

	switch builtinName(info, c.Fun) {
	case builtinAppend:
		for _, a := range c.Args {
			if !res.collect(fi, a) {
				return false
			}
		}

		return true
	case "make":
		return true
	}

	return res.fail(fi, c, "a call's result")
}

const builtinAppend = "append"

func builtinName(info *types.Info, fun ast.Expr) string {
	id, ok := ast.Unparen(fun).(*ast.Ident)
	if !ok {
		return ""
	}

	if b, ok := info.Uses[id].(*types.Builtin); ok {
		return b.Name()
	}

	return ""
}

func varKey(v *types.Var) string {
	if packageVar(v) {
		return "pkg:" + v.Pkg().Path() + "." + v.Name()
	}

	return "local:" + v.Name() + "@" + strconv.Itoa(int(v.Pos()))
}

// variable adds what a variable can hold: what each write puts in it,
// when every use of it is one the reading follows.
func (res *resolution) variable(fi *fileIndex, v *types.Var, at ast.Node) bool {
	key := varKey(v)
	if res.seen[key] {
		return true
	}

	res.seen[key] = true

	if packageVar(v) {
		return res.packageVar(fi, v, at)
	}

	def := fi.defs[v]
	if def == nil {
		return res.fail(fi, at, "a variable whose declaration the reading can't follow")
	}

	if !res.declared(fi, v, def) {
		return false
	}

	for _, u := range fi.uses[v] {
		if !res.use(fi, u, shares(v.Type())) {
			return false
		}
	}

	return true
}

// packageVar reads an unexported variable of the package in every file of
// it the build compiles; a file it leaves out could write it too.
func (res *resolution) packageVar(fi *fileIndex, v *types.Var, at ast.Node) bool {
	if v.Exported() {
		return res.fail(fi, at, "an exported variable, which other packages may write")
	}

	dir, ok := res.r.t.mods.dirOf(v.Pkg().Path())
	if !ok {
		return res.fail(fi, at, "a variable of another package")
	}

	if p := res.r.t.pkgs[dir]; p == nil || len(p.Excluded) > 0 {
		return res.fail(fi, at, "a variable of a package with files the build leaves out, which may write it")
	}

	found := false

	same := func(o types.Object) bool {
		return o != nil && o.Pkg() != nil && o.Pkg().Path() == v.Pkg().Path()
	}

	for _, w := range res.r.byDir[dir] {
		other := res.r.index(w)

		if def := other.pkgDefs[v.Name()]; def != nil && same(w.info.Defs[def]) {
			found = true

			if !res.declared(other, nil, def) {
				return false
			}
		}

		for _, u := range other.pkgUses[v.Name()] {
			if !same(w.info.Uses[u]) {
				continue
			}

			if !res.use(other, u, shares(v.Type())) {
				return false
			}
		}
	}

	if !found {
		return res.fail(fi, at, "a variable whose declaration the reading can't follow")
	}

	return true
}

func (res *resolution) declared(fi *fileIndex, v *types.Var, def *ast.Ident) bool {
	switch p := fi.parent[def].(type) {
	case *ast.ValueSpec:
		i := identIndex(p.Names, def)

		switch {
		case len(p.Values) == 0:
			return true
		case len(p.Values) == len(p.Names):
			return res.collect(fi, p.Values[i])
		}

		return res.fail(fi, def, "a variable declared from a call's results")
	case *ast.AssignStmt:
		return res.assigned(fi, p, exprIndex(p.Lhs, def), def)
	case *ast.RangeStmt:
		return res.ranged(fi, p, def)
	case *ast.Field:
		return res.param(fi, v, p, def)
	}

	return res.fail(fi, def, "a variable whose declaration the reading can't follow")
}

func (res *resolution) assigned(fi *fileIndex, as *ast.AssignStmt, i int, at ast.Node) bool {
	if as.Tok != token.ASSIGN && as.Tok != token.DEFINE {
		if holes(fi.w.info.TypeOf(as.Lhs[i])) {
			return res.fail(fi, at, "an operation the reading can't follow")
		}

		return true
	}

	if len(as.Lhs) == len(as.Rhs) {
		return res.collect(fi, as.Rhs[i])
	}

	// v, ok := m[k] or x.(T): the value is what the one right side is, ok
	// a bool; any other pair comes from a call
	if len(as.Rhs) == 1 && len(as.Lhs) == 2 {
		// the pair's type is recorded as a tuple: read what the value
		// comes from
		switch r := ast.Unparen(as.Rhs[0]).(type) {
		case *ast.IndexExpr:
			if i == 1 {
				return true
			}

			if !container(fi.w.info.TypeOf(r.X)) {
				return res.fail(fi, at, "an element of what the reading can't follow")
			}

			return res.collect(fi, r.X)
		case *ast.TypeAssertExpr:
			if i == 1 {
				return true
			}

			return res.collect(fi, r.X)
		}
	}

	return res.fail(fi, at, "a variable assigned from a call's results")
}

func (res *resolution) ranged(fi *fileIndex, rs *ast.RangeStmt, at ast.Node) bool {
	if !container(fi.w.info.TypeOf(rs.X)) {
		return res.fail(fi, at, "a value a range over no slice, array or map yields")
	}

	return res.collect(fi, rs.X)
}

// param adds what a closure's parameter gets: the arguments of every call
// of the closure, when it is called right where it is written or bound
// to a variable that is only ever called.
func (res *resolution) param(fi *fileIndex, v *types.Var, f *ast.Field, def *ast.Ident) bool {
	lit, idx, ok := closureOf(fi, f, def)
	if !ok {
		if res.methodParam(fi, v, f, def) {
			return true
		}

		return res.fail(fi, def, "a parameter of a function others can call")
	}

	if v != nil && lit.Type.Params != nil {
		if sig, ok := fi.w.info.TypeOf(lit).(*types.Signature); ok && sig.Variadic() && idx == sig.Params().Len()-1 {
			return res.fail(fi, def, "a closure's variadic parameter")
		}
	}

	calls, ok := closureCalls(fi, lit)
	if !ok {
		return res.fail(fi, def, "a parameter of a closure used as a value")
	}

	for _, c := range calls {
		if c.Ellipsis.IsValid() || idx >= len(c.Args) {
			return res.fail(fi, c, "a closure called in a way the reading can't follow")
		}

		if !res.collect(fi, c.Args[idx]) {
			return false
		}
	}

	return true
}

// ArgsNode is the junction of what the calls of a method of the tree pass
// to a parameter of it that can hold anything.
func ArgsNode(id goid.ID) string { return "node:go:args:" + string(id) }

// methodParam reads a method's parameter that can hold anything, handed on as
// it is: a value reaches it only through the method's args junction or, by
// name, the handed-by-name junction.
func (res *resolution) methodParam(fi *fileIndex, v *types.Var, f *ast.Field, def *ast.Ident) bool {
	list, ok := fi.parent[f].(*ast.FieldList)
	if !ok || v == nil || !emptyInterface(v.Type()) {
		return false
	}

	ft, ok := fi.parent[list].(*ast.FuncType)
	if !ok || ft.Params != list {
		return false
	}

	fd, ok := fi.parent[ft].(*ast.FuncDecl)
	if !ok || fd.Recv == nil {
		return false
	}

	fn, ok := fi.w.info.Defs[fd.Name].(*types.Func)
	if !ok || fn.Signature().Variadic() && identIndex(list.List[len(list.List)-1].Names, def) >= 0 {
		return false
	}

	id, ok := res.r.idx.Object(res.r.fset, fn)
	if !ok {
		return false
	}

	file := res.r.fset.Position(fd.Pos()).Filename
	res.via = append(res.via, forward{node: ArgsNode(id), name: fn.Name(), test: strings.HasSuffix(file, "_test.go")})

	return true
}

func emptyInterface(tt types.Type) bool {
	it, ok := types.Unalias(tt).Underlying().(*types.Interface)

	return ok && it.NumMethods() == 0
}

func closureOf(fi *fileIndex, f *ast.Field, def *ast.Ident) (*ast.FuncLit, int, bool) {
	list, ok := fi.parent[f].(*ast.FieldList)
	if !ok {
		return nil, 0, false
	}

	ft, ok := fi.parent[list].(*ast.FuncType)
	if !ok || ft.Params != list {
		return nil, 0, false
	}

	lit, ok := fi.parent[ft].(*ast.FuncLit)
	if !ok {
		return nil, 0, false
	}

	idx := 0

	for _, field := range list.List {
		for _, n := range field.Names {
			if n == def {
				return lit, idx, true
			}

			idx++
		}

		if len(field.Names) == 0 {
			idx++
		}
	}

	return nil, 0, false
}

// closureCalls are every call of a func literal: the one call it is
// written in, or the calls of the variable it is bound to, when that
// variable is never used otherwise and never assigned again.
func closureCalls(fi *fileIndex, lit *ast.FuncLit) ([]*ast.CallExpr, bool) {
	var (
		n ast.Node = lit
		p ast.Node
	)

	for p = fi.parent[n]; ; p = fi.parent[n] {
		if paren, ok := p.(*ast.ParenExpr); ok {
			n = paren

			continue
		}

		break
	}

	var bound *ast.Ident

	switch p := p.(type) {
	case *ast.CallExpr:
		if p.Fun == n {
			return []*ast.CallExpr{p}, true
		}

		return nil, false
	case *ast.AssignStmt:
		if p.Tok != token.DEFINE || len(p.Lhs) != len(p.Rhs) {
			return nil, false
		}

		if id, ok := p.Lhs[exprIndex(p.Rhs, n)].(*ast.Ident); ok {
			bound = id
		}
	case *ast.ValueSpec:
		if len(p.Names) != len(p.Values) {
			return nil, false
		}

		bound = p.Names[exprIndex(p.Values, n)]
	}

	if bound == nil {
		return nil, false
	}

	f, ok := fi.w.info.Defs[bound].(*types.Var)
	if !ok || packageVar(f) {
		return nil, false
	}

	var calls []*ast.CallExpr

	for _, u := range fi.uses[f] {
		var at ast.Node = u
		for {
			if paren, ok := fi.parent[at].(*ast.ParenExpr); ok {
				at = paren

				continue
			}

			break
		}

		c, ok := fi.parent[at].(*ast.CallExpr)
		if !ok || c.Fun != at {
			return nil, false
		}

		calls = append(calls, c)
	}

	return calls, true
}

func identIndex(ids []*ast.Ident, id *ast.Ident) int {
	for i, x := range ids {
		if x == id {
			return i
		}
	}

	return -1
}

func exprIndex(xs []ast.Expr, x ast.Node) int {
	for i, e := range xs {
		if e == x {
			return i
		}
	}

	return -1
}

// use reads one use of a variable, n: shared is set while n's value shares
// what it holds with the variable (the variable itself, a slice of it, an
// element that is a slice or a map). A write adds what it writes; a use
// the reading can't follow makes the variable unknown.
func (res *resolution) use(fi *fileIndex, n ast.Expr, shared bool) bool {
	info := fi.w.info

	// the hand-off itself
	if n == res.site {
		return true
	}

	switch p := fi.parent[n].(type) {
	case *ast.ParenExpr:
		return res.use(fi, p, shared)
	case *ast.AssignStmt:
		return res.inAssign(fi, p, n, shared)
	case *ast.ValueSpec:
		if !shared {
			return true
		}

		if i := exprIndex(p.Values, n); i >= 0 && len(p.Names) == len(p.Values) {
			return res.alias(fi, p.Names[i])
		}

		return res.fail(fi, n, "stored where the reading can't follow")
	case *ast.IndexExpr:
		if p.X != n {
			return true // a key or an index: a copy
		}

		return res.use(fi, p, shares(info.TypeOf(p)))
	case *ast.IndexListExpr:
		return true
	case *ast.SliceExpr:
		if p.X != n {
			return true
		}

		return res.use(fi, p, shared)
	case *ast.StarExpr:
		return res.use(fi, p, shared)
	case *ast.RangeStmt:
		return res.inRange(fi, p, n)
	case *ast.CallExpr:
		return res.argument(fi, p, n, shared)
	case *ast.UnaryExpr:
		if p.Op == token.AND {
			return res.fail(fi, n, "its address is taken")
		}

		return true
	case *ast.SelectorExpr, *ast.TypeAssertExpr, *ast.BinaryExpr, *ast.IfStmt, *ast.SwitchStmt,
		*ast.CaseClause, *ast.ExprStmt, *ast.IncDecStmt:
		return true
	}

	if !shared {
		return true
	}

	return res.fail(fi, n, "a slice or a map used where the reading can't follow")
}

func (res *resolution) inAssign(fi *fileIndex, as *ast.AssignStmt, n ast.Expr, shared bool) bool {
	if i := exprIndex(as.Lhs, n); i >= 0 {
		return res.assigned(fi, as, i, n)
	}

	if !shared {
		return true
	}

	if i := exprIndex(as.Rhs, n); i >= 0 && len(as.Lhs) == len(as.Rhs) {
		return res.alias(fi, as.Lhs[i])
	}

	return res.fail(fi, n, "stored where the reading can't follow")
}

// inRange reads a use of a variable in a range: a write as its key or
// value, and over it, an alias of the elements its value shares.
func (res *resolution) inRange(fi *fileIndex, rs *ast.RangeStmt, n ast.Expr) bool {
	if rs.Key == n || rs.Value == n {
		if rs.Tok == token.ASSIGN {
			return res.ranged(fi, rs, n)
		}

		return true
	}

	if rs.Value != nil && shares(fi.w.info.TypeOf(rs.Value)) {
		return res.alias(fi, rs.Value)
	}

	return true
}

func (res *resolution) argument(fi *fileIndex, c *ast.CallExpr, n ast.Expr, shared bool) bool {
	info := fi.w.info

	if c.Fun == n {
		return true
	}

	if tv, ok := info.Types[c.Fun]; ok && tv.IsType() {
		return res.use(fi, c, shared)
	}

	switch builtinName(info, c.Fun) {
	case "len", "cap", "delete", "clear", "print", "println", "min", "max":
		return true
	case builtinAppend:
		if len(c.Args) > 0 && c.Args[0] == n {
			// the result holds the same elements
			return res.use(fi, c, shared)
		}

		if !shared || (c.Ellipsis.IsValid() && c.Args[len(c.Args)-1] == n) {
			return true
		}

		return res.fail(fi, n, "put into another slice")
	case "copy":
		if len(c.Args) == 2 && c.Args[0] == n {
			return res.collect(fi, c.Args[1])
		}

		return true
	case "":
	default:
		return true
	}

	if !shared {
		return true
	}

	return res.fail(fi, n, "a slice or a map handed to a call, which may fill it")
}

// alias joins the variable a shared value is stored in: its writes write
// the same elements.
func (res *resolution) alias(fi *fileIndex, lhs ast.Expr) bool {
	root := ast.Unparen(lhs)

	for {
		switch x := root.(type) {
		case *ast.IndexExpr:
			root = ast.Unparen(x.X)

			continue
		case *ast.Ident:
			if x.Name == "_" {
				return true
			}

			o := fi.w.info.Uses[x]
			if o == nil {
				o = fi.w.info.Defs[x]
			}

			if v, ok := o.(*types.Var); ok {
				return res.variable(fi, v, x)
			}
		}

		return res.fail(fi, lhs, "stored where the reading can't follow")
	}
}
