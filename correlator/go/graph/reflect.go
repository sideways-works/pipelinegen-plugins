package graph

import (
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"strconv"
	"strings"
)

// reflectSites applies the per-site rule to a file's own reflection over
// methods: a function that enumerates methods or looks one up by a computed
// name costs the whole method set of the types it hands to reflect, for the
// binaries that link the package. One that hands no value over costs every
// type's; a call that can only read an interface's methods costs nothing.
func (t *Tree) reflectSites(r *reacher, w work) {
	file := r.fset.Position(w.file.Package).Filename

	from := GroupNode(r.dir)
	if strings.HasSuffix(file, "_test.go") {
		from = TestNode(r.dir)
	}

	for _, d := range w.file.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}

		site := t.reflectSite(w.info, r.fset, w.file, fd)
		if site == "" {
			continue
		}

		handed := false

		ast.Inspect(fd.Body, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			switch reflectCall(w.info, c) {
			case "ValueOf", "TypeOf":
				if len(c.Args) == 1 {
					handed = true

					if tv, ok := w.info.Types[c.Args[0]]; ok && tv.Type != nil {
						r.components(from, tv.Type, false, "handed to reflection at "+site)
					}
				}
			case "TypeFor":
				if inst, ok := w.info.Instances[typeForIdent(c)]; ok && inst.TypeArgs.Len() == 1 {
					handed = true

					r.components(from, inst.TypeArgs.At(0), false, "handed to reflection at "+site)
				}
			}

			return true
		})

		if !handed {
			r.add(from, AllMethodsNode)
			t.forced[from] = append(t.forced[from], "reflection at "+site+" over values its function doesn't hand over")
		}
	}
}

func reflectCall(info *types.Info, c *ast.CallExpr) string {
	fn, ok := funcOf(info, c.Fun).(*types.Func)
	if !ok {
		if ix, ok := ast.Unparen(c.Fun).(*ast.IndexExpr); ok {
			fn, ok = funcOf(info, ix.X).(*types.Func)
			if !ok {
				return ""
			}
		} else {
			return ""
		}
	}

	if fn.Pkg() == nil || fn.Pkg().Path() != reflectPkg || fn.Signature().Recv() != nil {
		return ""
	}

	return fn.Name()
}

// typeForIdent is the identifier of reflect.TypeFor in a call
// reflect.TypeFor[T](), whose instance holds T.
func typeForIdent(c *ast.CallExpr) *ast.Ident {
	ix, ok := ast.Unparen(c.Fun).(*ast.IndexExpr)
	if !ok {
		return nil
	}

	switch x := ast.Unparen(ix.X).(type) {
	case *ast.SelectorExpr:
		return x.Sel
	case *ast.Ident:
		return x
	}

	return nil
}

// reflectSite is the first place in a function that reads methods by
// reflection in a way no name tells, empty when there is none.
func (t *Tree) reflectSite(info *types.Info, fset *token.FileSet, file *ast.File, fd *ast.FuncDecl) string {
	consts := constsOf(file)

	defs := singleDefs(info, fd.Body)
	site := ""

	var stack []ast.Node

	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]

			return true
		}

		if c, ok := n.(*ast.CallExpr); ok && site == "" && !provable(info, c, stack, defs, consts) {
			p := fset.Position(c.Pos())
			site = filepath.Base(p.Filename) + ":" + strconv.Itoa(p.Line)
		}

		stack = append(stack, n)

		return true
	})

	return site
}

// provable tells whether a call reads no concrete type's methods in a way
// no name tells: it is no reflective method lookup at all, or one of the
// shapes that only reads an interface's methods or a name N holds.
func provable(info *types.Info, c *ast.CallExpr, stack []ast.Node, defs map[types.Object]ast.Expr, consts map[string][]string) bool {
	sel, ok := ast.Unparen(c.Fun).(*ast.SelectorExpr)
	if !ok || !isReflectValue(info, sel.X) {
		return true
	}

	switch sel.Sel.Name {
	case "NumMethod":
		return len(c.Args) != 0 || interfaceOnly(sel.X, stack) || gateOnly(c, stack) || ifaceType(info, sel.X, defs)
	case methodSel:
		return len(c.Args) != 1 || interfaceOnly(sel.X, stack) || ifaceType(info, sel.X, defs)
	case methodByName:
		return len(c.Args) != 1 || fileConst(info, c.Args[0], consts) || ifaceMethodName(info, c.Args[0], defs)
	}

	return true
}

func isReflectValue(info *types.Info, x ast.Expr) bool {
	tv, ok := info.Types[x]
	if !ok || tv.Type == nil {
		return false
	}

	named, ok := types.Unalias(tv.Type).(*types.Named)

	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == reflectPkg &&
		(named.Obj().Name() == "Value" || named.Obj().Name() == "Type")
}

func singleDefs(info *types.Info, body *ast.BlockStmt) map[types.Object]ast.Expr {
	out := map[types.Object]ast.Expr{}
	again := map[types.Object]bool{}

	ast.Inspect(body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}

		for i, lhs := range as.Lhs {
			id, ok := lhs.(*ast.Ident)
			if !ok {
				continue
			}

			if o := info.Defs[id]; o != nil && as.Tok == token.DEFINE && len(as.Lhs) == len(as.Rhs) {
				out[o] = as.Rhs[i]
			} else if o := info.Uses[id]; o != nil {
				again[o] = true
			}
		}

		return true
	})

	for o := range again {
		delete(out, o)
	}

	return out
}

// ifaceType tells whether x is the reflect.Type of an interface type:
// reflect.TypeFor[I](), reflect.TypeOf((*I)(nil)).Elem(), or a local
// defined once as one of them.
func ifaceType(info *types.Info, x ast.Expr, defs map[types.Object]ast.Expr) bool {
	switch x := ast.Unparen(x).(type) {
	case *ast.Ident:
		if e, ok := defs[info.Uses[x]]; ok {
			return ifaceType(info, e, defs)
		}
	case *ast.CallExpr:
		if reflectCall(info, x) == "TypeFor" {
			inst, ok := info.Instances[typeForIdent(x)]

			return ok && inst.TypeArgs.Len() == 1 && types.IsInterface(inst.TypeArgs.At(0))
		}

		sel, ok := ast.Unparen(x.Fun).(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Elem" || len(x.Args) != 0 {
			return false
		}

		inner, ok := ast.Unparen(sel.X).(*ast.CallExpr)
		if !ok || reflectCall(info, inner) != "TypeOf" || len(inner.Args) != 1 {
			return false
		}

		if tv, ok := info.Types[inner.Args[0]]; ok {
			if p, ok := tv.Type.(*types.Pointer); ok {
				return types.IsInterface(p.Elem())
			}
		}
	}

	return false
}

// ifaceMethodName tells whether x is api.Method(i).Name for api the
// reflect.Type of an interface: a name of the interface, which N holds.
func ifaceMethodName(info *types.Info, x ast.Expr, defs map[types.Object]ast.Expr) bool {
	switch e := ast.Unparen(x).(type) {
	case *ast.Ident:
		if d, ok := defs[info.Uses[e]]; ok {
			return ifaceMethodName(info, d, defs)
		}
	case *ast.SelectorExpr:
		if e.Sel.Name != "Name" {
			return false
		}

		c, ok := ast.Unparen(e.X).(*ast.CallExpr)
		if !ok || len(c.Args) != 1 {
			return false
		}

		sel, ok := ast.Unparen(c.Fun).(*ast.SelectorExpr)

		return ok && sel.Sel.Name == "Method" && ifaceType(info, sel.X, defs)
	}

	return false
}

// fileConst tells whether x is a string literal, or a package constant
// its file declares as literals: the names set reads those from syntax
// into N.
func fileConst(info *types.Info, x ast.Expr, consts map[string][]string) bool {
	if _, ok := stringLit(x); ok {
		return true
	}

	id, ok := ast.Unparen(x).(*ast.Ident)
	if !ok {
		return false
	}

	c, ok := info.Uses[id].(*types.Const)
	if !ok || c.Pkg() == nil || c.Parent() != c.Pkg().Scope() {
		return false
	}

	_, ok = consts[id.Name]

	return ok
}
