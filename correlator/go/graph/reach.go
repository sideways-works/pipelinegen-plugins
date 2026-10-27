package graph

import (
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/sideways-works/pipelinegen-sbom-go/goid"
)

// A dependency can't import the tree's packages, so it holds only the values
// of the tree's types the tree hands to it, and what those reach. When it
// reads methods in a way no name tells (a template whose text the scan can't
// read), the whole method set is owed only by the types that can reach it
// that way. These junctions carry it:
//
//	node:go:handed-to:<package>   what the tree hands to a package outside it
//	node:go:handed-by-name:<M>    what the tree hands to an interface's method M,
//	                              which a dependency's type may implement
//	node:go:handed-by-func        what the tree hands to a func value, which a
//	                              dependency may have made
//	node:go:reach:<type>          a type of the tree and what its values reach
//	node:go:has-method:<M>        every type of the tree with a method M: the
//	                              values an interface with M can hold
//
// A hand-off runs only in the binaries that link it, so a package's code
// hands to its own copy of a junction (inGroup) and a test file to its test
// binary's (inTest). What any code hands to a func value stays in the one
// handed-by-func.

// HandedTo is the junction of what the tree hands to a package outside it.
func HandedTo(pkg string) string { return handedToPrefix + pkg }

const handedToPrefix = "node:go:handed-to:"

func handedByName(m string) string { return "node:go:handed-by-name:" + m }

const handedByFunc = "node:go:handed-by-func"

// inTest is the junction j of one test binary: what a test file hands over
// is handed only in its package's test binary.
func inTest(j, dir string) string { return j + "@test:" + dir }

// inGroup is the junction j of one package's code: what it hands to a
// dependency, which only the binaries that link the package hand over.
func inGroup(j, dir string) string { return j + "@group:" + dir }

// assertsFunc is the junction of code that asserts a func type out of an
// interface: the func may have been made by a package that reads methods
// in ways no name tells, so the names set links it to handed-by-func when
// the tree links any such package.
const assertsFunc = "node:go:asserts-func"

// fromOf is the junction a file's code is linked into binaries by: its
// package's group, or its test binary's node for a test file.
func fromOf(dir, file string) string {
	if strings.HasSuffix(file, "_test.go") {
		return TestNode(dir)
	}

	return GroupNode(dir)
}

func reachNode(id goid.ID) string { return "node:go:reach:" + string(id) }

// hasMethodNode is the junction of the types of the tree with a method:
// by its name and signature, which a type must match exactly to
// implement an interface; a generic type's methods, whose signatures hold
// its type parameters, by name alone.
func hasMethodNode(m string, sig *types.Signature) string {
	if sig == nil {
		return "node:go:has-method:" + m
	}

	return "node:go:has-method:" + m + " " + types.TypeString(sig, nil)
}

// junctionLink is a link of the junctions: it holds only while
// interface-methods is on, since with it off every type a slice uses binds
// its whole method set anyway.
func junctionLink(from, to string) Link {
	return Link{From: from, To: to, Kind: KindFollow, Narrowing: NarrowInterfaceMethods, While: WhileOn}
}

type reacher struct {
	t    *Tree
	fset *token.FileSet
	idx  *goid.Index
	dir  string
}

func (r *reacher) add(from, to string) {
	l := junctionLink(from, to)
	if !slices.Contains(r.t.extra[r.dir], l) {
		r.t.extra[r.dir] = append(r.t.extra[r.dir], l)
	}
}

func (r *reacher) decl(tn *types.TypeName) (int32, bool) {
	if tn.Pkg() == nil {
		return 0, false
	}

	if _, ok := r.t.mods.dirOf(tn.Pkg().Path()); !ok {
		return 0, false
	}

	if r.t.syntax[tn.Pkg()] {
		id, ok := r.idx.Object(r.fset, tn)
		if !ok {
			return 0, false
		}

		i, ok := r.t.byID[id]

		return i, ok
	}

	return r.t.externalID(tn)
}

// components links from to what a value of type tt can reach, without
// entering the tree's named types, each of which has a reach junction of its
// own. own is set inside a type's own declaration.
func (r *reacher) components(from string, tt types.Type, own bool, why string) {
	switch tt := tt.(type) {
	case *types.Alias:
		r.components(from, types.Unalias(tt), own, why)
	case *types.Named:
		if i, ok := r.decl(tt.Origin().Obj()); ok {
			r.add(from, reachNode(r.t.Decls[i].ID))
		} else if it, ok := tt.Underlying().(*types.Interface); ok {
			r.iface(from, it, why+" ("+tt.String()+")")
		}

		for a := range tt.TypeArgs().Types() {
			r.components(from, a, own, why)
		}
	case *types.Pointer:
		r.components(from, tt.Elem(), own, why)
	case *types.Slice:
		r.components(from, tt.Elem(), own, why)
	case *types.Array:
		r.components(from, tt.Elem(), own, why)
	case *types.Chan:
		r.components(from, tt.Elem(), own, why)
	case *types.Map:
		r.components(from, tt.Key(), own, why)
		r.components(from, tt.Elem(), own, why)
	case *types.Signature:
		r.components(from, tt.Results(), own, why)
	case *types.Tuple:
		for v := range tt.Variables() {
			r.components(from, v.Type(), own, why)
		}
	case *types.Struct:
		for f := range tt.Fields() {
			r.components(from, f.Type(), own, why)
		}
	case *types.Interface:
		r.iface(from, tt, why+" ("+tt.String()+")")
	case *types.TypeParam:
		if !own {
			if it, ok := tt.Constraint().Underlying().(*types.Interface); ok {
				r.iface(from, it, why+" (type parameter "+tt.String()+")")
			}
		}
	}
}

// iface links from to the types an interface's values can be: every type
// of the tree with its first method, or, for an interface without methods,
// every type.
func (r *reacher) iface(from string, it *types.Interface, why string) {
	if it.NumMethods() == 0 {
		r.add(from, AllMethodsNode)

		if !slices.Contains(r.t.forced[from], why) {
			r.t.forced[from] = append(r.t.forced[from], why)
		}

		return
	}

	// one method is enough to find every implementation: the first by name
	var first *types.Func

	for m := range it.Methods() {
		if first == nil || m.Name() < first.Name() {
			first = m
		}
	}

	r.add(from, hasMethodNode(first.Name(), first.Signature()))
	r.add(from, hasMethodNode(first.Name(), nil))
}

func (r *reacher) reachType(tn *types.TypeName) {
	i, ok := r.decl(tn)
	if !ok {
		return
	}

	d := r.t.Decls[i]
	from := reachNode(d.ID)

	r.add(from, string(d.ID))

	if r.t.hasTwin(i) || r.t.hasDecl(i) {
		r.add(from, r.t.twin(i))
	}

	named, ok := tn.Type().(*types.Named)
	if !ok {
		return
	}

	why := "a value of " + string(d.ID)

	r.components(from, named.Underlying(), true, why)

	for m := range named.Methods() {
		r.results(from, m, why)
	}

	if types.IsInterface(named) {
		return
	}

	generic := named.TypeParams().Len() > 0

	ms := types.NewMethodSet(types.NewPointer(named))
	for sel := range ms.Methods() {
		fn, ok := sel.Obj().(*types.Func)
		if !ok {
			continue
		}

		if generic {
			r.add(hasMethodNode(fn.Name(), nil), from)
		} else {
			r.add(hasMethodNode(fn.Name(), fn.Signature()), from)
		}
	}
}

// results links from to what the results of a method can hold: for an
// interface, or a slice or a map of them, what its return statements put
// there, when that can be told.
func (r *reacher) results(from string, m *types.Func, why string) {
	rs := m.Signature().Results()

	for i := range rs.Len() {
		tt := rs.At(i).Type()

		// an interface's methods have no body to read
		if holes(tt) && r.t.resolver != nil && !types.IsInterface(m.Signature().Recv().Type()) {
			ts, unknown := r.t.resolver.results(r.dir, m, i)
			if unknown == "" {
				for _, x := range ts {
					r.components(from, x, true, why)
				}

				continue
			}

			r.components(from, tt, true, why+", the result of "+m.Name()+": "+unknown)

			continue
		}

		r.components(from, tt, true, why)
	}
}

func (t *Tree) handoffs(fset *token.FileSet, idx *goid.Index, w work) {
	file := fset.Position(w.file.Package).Filename
	h := &handoff{
		reacher: &reacher{t: t, fset: fset, idx: idx, dir: relPath(t.Root, filepath.Dir(file))},
		info:    w.info, file: file, written: map[ast.Expr]bool{}, called: map[ast.Expr]bool{},
		test: strings.HasSuffix(file, "_test.go"), w: w,
	}

	if t.resolver != nil {
		t.resolver.enter(h.dir)
	}

	for _, d := range w.file.Decls {
		if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.TYPE {
			for _, spec := range g.Specs {
				if tn, ok := w.info.Defs[spec.(*ast.TypeSpec).Name].(*types.TypeName); ok && !tn.IsAlias() {
					h.reachType(tn)
				}
			}
		}
	}

	ast.Inspect(w.file, h.node)

	t.reflectSites(h.reacher, w)
}

type handoff struct {
	*reacher

	info *types.Info
	file string
	// written are the left sides of assignments: a field written is no
	// func obtained
	written map[ast.Expr]bool
	// called are the functions of calls: a method selected to be called
	// is no method value
	called map[ast.Expr]bool
	// test is set for a test file: what it hands over counts only in its
	// package's test binary
	test bool
	w    work
}

func (h *handoff) to(j string) string {
	switch {
	case j == handedByFunc:
	case h.test:
		return inTest(j, h.dir)
	case strings.HasPrefix(j, handedToPrefix):
		return inGroup(j, h.dir)
	}

	return j
}

func (h *handoff) at(n ast.Node) string {
	p := h.fset.Position(n.Pos())

	return filepath.Base(p.Filename) + ":" + strconv.Itoa(p.Line)
}

// hand notes a value that lands in a slot outside the tree. Only a slot that
// can hold one of the tree's types can hand one over.
func (h *handoff) hand(from string, x ast.Expr, slot types.Type) {
	if slot != nil && !mayHold(slot) {
		return
	}

	tv, ok := h.info.Types[x]
	if !ok || tv.Type == nil {
		return
	}

	why := "handed to " + strings.TrimPrefix(from, "node:go:") + " at " + h.at(x)

	// an interface, or a slice or a map of them: what the code around it
	// puts in it, when that can be told
	if holes(tv.Type) && h.t.resolver != nil {
		ts, via, unknown := h.t.resolver.resolve(h.w, x)
		if unknown == "" {
			for _, tt := range ts {
				h.components(from, tt, false, why)
			}

			for _, f := range via {
				h.forwards(from, f)
			}

			return
		}

		why += ", " + unknown
	}

	h.components(from, tv.Type, false, why)
}

// forwards links what a method's parameter handed on as it is can hold:
// what its tree call sites pass, and what is passed to an interface's
// method of its name, by the tree's code or a test file of its own.
func (h *handoff) forwards(from string, f forward) {
	h.add(from, f.node)
	h.add(f.node, handedByName(f.name))

	if f.test {
		h.add(f.node, inTest(handedByName(f.name), h.dir))
	}
}

// direct hands the arguments of a call of a method of the tree that land
// in a parameter able to hold anything to the method's args junction: a
// method that hands such a parameter on reads them from there.
func (h *handoff) direct(c *ast.CallExpr) {
	fn, ok := funcOf(h.info, c.Fun).(*types.Func)
	if !ok {
		return
	}

	sig := fn.Origin().Signature()
	if sig.Recv() == nil || types.IsInterface(sig.Recv().Type()) || !anyParam(sig) {
		return
	}

	id, ok := h.funcID(fn.Origin())
	if !ok {
		return
	}

	for i, a := range args(h.info, c) {
		if p := paramAt(sig, i, c.Ellipsis.IsValid()); p != nil && (emptyInterface(p) || emptySlice(p)) {
			h.hand(ArgsNode(id), a, p)
		}
	}
}

func anyParam(sig *types.Signature) bool {
	for v := range sig.Params().Variables() {
		if emptyInterface(v.Type()) || emptySlice(v.Type()) {
			return true
		}
	}

	return false
}

func emptySlice(tt types.Type) bool {
	s, ok := types.Unalias(tt).(*types.Slice)

	return ok && emptyInterface(s.Elem())
}

func (r *reacher) funcID(fn *types.Func) (goid.ID, bool) {
	if fn.Pkg() == nil {
		return "", false
	}

	if _, ok := r.t.mods.dirOf(fn.Pkg().Path()); !ok {
		return "", false
	}

	if r.t.syntax[fn.Pkg()] {
		return r.idx.Object(r.fset, fn)
	}

	i, ok := r.t.externalID(fn)
	if !ok {
		return "", false
	}

	return r.t.Decls[i].ID, true
}

// methodValue notes a method of the tree taken as a value, or an
// interface's method expression: what is later passed to the func value
// reaches the method's parameters, which the handed-by-func junction
// collects.
func (h *handoff) methodValue(n *ast.SelectorExpr) {
	sel := h.info.Selections[n]
	if sel == nil || (sel.Kind() == types.MethodVal && h.called[n]) {
		return
	}

	fn, ok := sel.Obj().(*types.Func)
	if !ok || !anyParam(fn.Signature()) {
		return
	}

	// a template's Execute taken as a value: what its calls pass is
	// handed to a func value, which may be a template whose text the scan
	// did not read
	if templateMethod(fn) && isExecute(fn.Name()) {
		if h.called[n] {
			return
		}

		if why := h.templateOf(h.w, n.X); sel.Kind() != types.MethodVal || why != "" {
			h.add(fromOf(h.dir, h.file), handedByFunc)
			h.t.executes = appendNew(h.t.executes, h.dir+"/"+h.at(n)+": "+fn.Name()+" taken as a value of a template the scan did not read")
		}

		return
	}

	if types.IsInterface(fn.Signature().Recv().Type()) {
		h.add(handedByName(fn.Name()), handedByFunc)

		return
	}

	if id, ok := h.funcID(fn.Origin()); ok {
		h.add(ArgsNode(id), handedByFunc)
	}
}

func (h *handoff) outside(pkg *types.Package) bool {
	if pkg == nil {
		return false
	}

	_, inTree := h.t.mods.dirOf(pkg.Path())

	return !inTree
}

func (h *handoff) node(n ast.Node) bool {
	switch n := n.(type) {
	case *ast.Ident:
		h.instance(n)
	case *ast.TypeAssertExpr:
		// a func value asserted out of an interface may have been made
		// outside the tree: the binaries of this code call such funcs
		if n.Type != nil && mayCarryFunc(h.info.TypeOf(n.Type)) {
			h.add(fromOf(h.dir, h.file), assertsFunc)
		}
	case *ast.CaseClause:
		for _, x := range n.List {
			if tv, ok := h.info.Types[x]; ok && tv.IsType() && mayCarryFunc(tv.Type) {
				h.add(fromOf(h.dir, h.file), assertsFunc)
			}
		}
	case *ast.SelectorExpr:
		h.methodValue(n)

		// a func value a package outside the tree holds in a variable or a
		// field: what the tree calls it with is handed to that package
		if v, ok := h.info.Uses[n.Sel].(*types.Var); ok && !h.written[n] && h.outside(v.Pkg()) && mayCarryFunc(v.Type()) {
			h.add(h.to(HandedTo(v.Pkg().Path())), h.to(handedByFunc))
		}
	case *ast.CallExpr:
		h.call(n)
	case *ast.AssignStmt:
		h.assign(n)
	case *ast.CompositeLit:
		h.composite(n)
	}

	return true
}

// instance notes a generic of outside the tree instantiated with the
// tree's types: its code sees them as its type parameters.
func (h *handoff) instance(id *ast.Ident) {
	inst, ok := h.info.Instances[id]
	if !ok {
		return
	}

	if o := h.info.Uses[id]; o != nil && h.outside(o.Pkg()) {
		for a := range inst.TypeArgs.Types() {
			h.components(h.to(HandedTo(o.Pkg().Path())), a, false, "instantiated at "+h.at(id))
		}
	}
}

func (h *handoff) composite(c *ast.CompositeLit) {
	pkg, st := h.t.outsideStruct(h.info, c)
	if pkg == "" {
		return
	}

	for i, el := range c.Elts {
		if kv, ok := el.(*ast.KeyValueExpr); ok {
			h.hand(h.to(HandedTo(pkg)), kv.Value, fieldType(st, kv.Key, -1))
		} else {
			h.hand(h.to(HandedTo(pkg)), el, fieldType(st, nil, i))
		}
	}
}

func (h *handoff) call(c *ast.CallExpr) {
	if pkg := h.t.outsideConversion(h.info, c); pkg != "" && len(c.Args) == 1 {
		// a struct converted to a type of outside the tree keeps the values
		// its fields hold
		h.hand(h.to(HandedTo(pkg)), c.Args[0], nil)

		return
	}

	h.called[ast.Unparen(c.Fun)] = true

	callee, sig := h.t.callee(h.info, c)
	if callee == "" {
		h.direct(c)

		return
	}

	from := h.to(callee)

	// a func value a package outside the tree returns
	if strings.HasPrefix(callee, handedToPrefix) && sig != nil && mayCarryFunc(sig.Results()) {
		h.add(from, h.to(handedByFunc))
	}

	// the data of a template whose text the scan did not read: wide,
	// wherever this code is linked
	data := -1
	if wide, why := h.unreadExecute(c); wide && sig != nil {
		data = sig.Params().Len() - 1
		h.t.executes = appendNew(h.t.executes, h.dir+"/"+h.at(c)+": "+why)
	}

	for i, a := range args(h.info, c) {
		if i == data {
			h.hand(fromOf(h.dir, h.file), a, paramAt(sig, i, c.Ellipsis.IsValid()))

			continue
		}

		h.hand(from, a, paramAt(sig, i, c.Ellipsis.IsValid()))
	}
}

// args are a call's arguments in the order of its method's parameters: a
// method expression's call, T.M(t, x), passes the receiver first.
func args(info *types.Info, c *ast.CallExpr) []ast.Expr {
	if sel, ok := ast.Unparen(c.Fun).(*ast.SelectorExpr); ok && len(c.Args) > 0 {
		if s := info.Selections[sel]; s != nil && s.Kind() == types.MethodExpr {
			return c.Args[1:]
		}
	}

	return c.Args
}

func (h *handoff) assign(as *ast.AssignStmt) {
	for _, lhs := range as.Lhs {
		h.written[ast.Unparen(lhs)] = true
	}

	if len(as.Lhs) != len(as.Rhs) {
		return
	}

	for i, lhs := range as.Lhs {
		if pkg := h.t.outsideSlot(h.info, lhs); pkg != "" {
			h.hand(h.to(HandedTo(pkg)), as.Rhs[i], h.info.TypeOf(lhs))
		}
	}
}

// callee is the junction a call hands its arguments to: a function or
// method outside the tree, an interface's method (its implementation may
// be outside), or a func value (it may have been made outside). A call of
// the tree's own code, a conversion or a builtin hands nothing over.
func (t *Tree) callee(info *types.Info, c *ast.CallExpr) (string, *types.Signature) {
	if tv, ok := info.Types[c.Fun]; ok && (tv.IsType() || tv.IsBuiltin()) {
		return "", nil
	}

	var obj types.Object

	switch fun := ast.Unparen(c.Fun).(type) {
	case *ast.Ident:
		obj = info.Uses[fun]
	case *ast.SelectorExpr:
		obj = info.Uses[fun.Sel]
	case *ast.IndexExpr:
		obj = funcOf(info, fun.X)
	case *ast.IndexListExpr:
		obj = funcOf(info, fun.X)
	}

	fn, ok := obj.(*types.Func)
	if !ok {
		// a package the type checker refuses may leave the callee untyped:
		// a nil signature hands every argument
		var sig *types.Signature
		if tt := info.TypeOf(c.Fun); tt != nil {
			sig, _ = tt.Underlying().(*types.Signature) //nolint:errcheck // as above
		}

		return handedByFunc, sig
	}

	// the generic's own signature: a parameter of a type parameter's type
	// holds whatever the call instantiates it with
	sig := fn.Origin().Signature()

	if recv := sig.Recv(); recv != nil && types.IsInterface(recv.Type()) {
		return handedByName(fn.Name()), sig
	}

	if fn.Pkg() == nil {
		return "", nil
	}

	if _, inTree := t.mods.dirOf(fn.Pkg().Path()); inTree {
		return "", nil
	}

	return HandedTo(fn.Pkg().Path()), sig
}

func paramAt(sig *types.Signature, i int, ellipsis bool) types.Type {
	if sig == nil {
		return nil
	}

	ps := sig.Params()

	switch {
	case sig.Variadic() && i >= ps.Len()-1 && !ellipsis:
		if s, ok := ps.At(ps.Len() - 1).Type().(*types.Slice); ok {
			return s.Elem()
		}

		return nil
	case i < ps.Len():
		return ps.At(i).Type()
	}

	return nil
}

func fieldType(st *types.Struct, key ast.Expr, i int) types.Type {
	if st == nil {
		return nil
	}

	if id, ok := key.(*ast.Ident); ok {
		for f := range st.Fields() {
			if f.Name() == id.Name {
				return f.Type()
			}
		}

		return nil
	}

	if i >= 0 && i < st.NumFields() {
		return st.Field(i).Type()
	}

	return nil
}

// mayCarryFunc tells whether a value of type tt can carry a func value the
// tree may call with its own values: a func whose parameters may hold one,
// or what is made of such funcs. A func hidden in an interface reaches a
// call only through an assertion to a func type, which assertsFunc links.
func mayCarryFunc(tt types.Type) bool {
	switch tt := tt.(type) {
	case *types.Alias:
		return mayCarryFunc(types.Unalias(tt))
	case *types.Signature:
		return mayHold(tt.Params())
	case *types.Named:
		if s, ok := tt.Underlying().(*types.Signature); ok {
			return mayHold(s.Params())
		}

		return false
	case *types.Pointer:
		return mayCarryFunc(tt.Elem())
	case *types.Slice:
		return mayCarryFunc(tt.Elem())
	case *types.Array:
		return mayCarryFunc(tt.Elem())
	case *types.Chan:
		return mayCarryFunc(tt.Elem())
	case *types.Map:
		return mayCarryFunc(tt.Key()) || mayCarryFunc(tt.Elem())
	case *types.Tuple:
		for v := range tt.Variables() {
			if mayCarryFunc(v.Type()) {
				return true
			}
		}
	case *types.Struct:
		for f := range tt.Fields() {
			if mayCarryFunc(f.Type()) {
				return true
			}
		}
	}

	return false
}

// mayHold tells whether a slot of type tt can hold a value of a type of the
// tree with its methods: an interface, a type parameter, or what is made of
// them. A named type outside the tree holds what its type arguments are.
func mayHold(tt types.Type) bool {
	switch tt := tt.(type) {
	case *types.Alias:
		return mayHold(types.Unalias(tt))
	case *types.Interface, *types.TypeParam:
		return true
	case *types.Named:
		if types.IsInterface(tt) {
			return true
		}

		for a := range tt.TypeArgs().Types() {
			if mayHold(a) {
				return true
			}
		}

		return false
	case *types.Pointer:
		return mayHold(tt.Elem())
	case *types.Slice:
		return mayHold(tt.Elem())
	case *types.Array:
		return mayHold(tt.Elem())
	case *types.Chan:
		return mayHold(tt.Elem())
	case *types.Map:
		return mayHold(tt.Key()) || mayHold(tt.Elem())
	case *types.Signature:
		return mayHold(tt.Results())
	case *types.Tuple:
		for v := range tt.Variables() {
			if mayHold(v.Type()) {
				return true
			}
		}
	case *types.Struct:
		for f := range tt.Fields() {
			if mayHold(f.Type()) {
				return true
			}
		}
	}

	return false
}

func funcOf(info *types.Info, x ast.Expr) types.Object {
	switch x := ast.Unparen(x).(type) {
	case *ast.Ident:
		return info.Uses[x]
	case *ast.SelectorExpr:
		return info.Uses[x.Sel]
	}

	return nil
}

func (t *Tree) outsideSlot(info *types.Info, lhs ast.Expr) string {
	for {
		switch x := ast.Unparen(lhs).(type) {
		case *ast.IndexExpr:
			lhs = x.X

			continue
		case *ast.SelectorExpr:
			if v, ok := info.Uses[x.Sel].(*types.Var); ok && v.Pkg() != nil {
				if _, inTree := t.mods.dirOf(v.Pkg().Path()); !inTree {
					return v.Pkg().Path()
				}
			}
		}

		return ""
	}
}

func (t *Tree) outsideConversion(info *types.Info, c *ast.CallExpr) string {
	tv, ok := info.Types[c.Fun]
	if !ok || !tv.IsType() {
		return ""
	}

	named, ok := types.Unalias(tv.Type).(*types.Named)
	if !ok || named.Obj().Pkg() == nil || !mayHold(named.Underlying()) {
		return ""
	}

	if _, inTree := t.mods.dirOf(named.Obj().Pkg().Path()); inTree {
		return ""
	}

	return named.Obj().Pkg().Path()
}

func (t *Tree) outsideStruct(info *types.Info, c *ast.CompositeLit) (string, *types.Struct) {
	tv, ok := info.Types[c]
	if !ok {
		return "", nil
	}

	named, ok := types.Unalias(tv.Type).(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return "", nil
	}

	st, isStruct := named.Underlying().(*types.Struct)
	if !isStruct {
		return "", nil
	}

	if _, inTree := t.mods.dirOf(named.Obj().Pkg().Path()); inTree {
		return "", nil
	}

	return named.Obj().Pkg().Path(), st
}

// Executes says, once each, where the tree executes a template whose text
// the scan did not read, and why it counts so.
func (t *Tree) Executes() []string {
	out := slices.Clone(t.executes)
	slices.Sort(out)

	return slices.Compact(out)
}

// Forced says, once each, what binds every type's whole method set where
// it matters: a value whose type is an interface without methods, reached
// from what the tree hands to a package that reads methods in ways no name
// tells, or reflection over values its function doesn't hand over.
func (t *Tree) Forced() []string {
	t.buildJunctions()

	var queue []string

	if t.names != nil {
		queue = append(queue, sortedKeys(t.names.Hands)...)

		for _, dir := range sortedKeys(t.names.tests) {
			queue = append(queue, TestNode(dir))
		}
	}

	for from := range t.forced {
		if strings.HasPrefix(from, "group:") || strings.HasPrefix(from, "node:go:test:") {
			queue = append(queue, from)
		}
	}

	seen := map[string]bool{}

	var out []string

	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]

		if seen[n] {
			continue
		}

		seen[n] = true

		out = append(out, t.forced[n]...)

		queue = append(queue, t.junctions[n]...)
	}

	slices.Sort(out)

	return slices.Compact(out)
}
