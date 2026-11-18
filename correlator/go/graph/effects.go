package graph

import (
	"go/ast"
	"go/token"
	"go/types"
)

// mayPanic tells whether evaluating an initialiser expression can panic or
// block when its package starts, without calling anything. A panic at start
// breaks every binary that links the package, so such an initialiser acts
// like a call outside the pure ones. commaOK says the expression is the
// single value of a two-name spec, whose type assertion doesn't panic.
func mayPanic(e ast.Expr, info *types.Info, commaOK bool) bool {
	found := false

	if commaOK {
		if ta, ok := ast.Unparen(e).(*ast.TypeAssertExpr); ok {
			// the assertion itself can't panic; its operand still can
			return mayPanic(ta.X, info, false)
		}
	}

	ast.Inspect(e, func(n ast.Node) bool {
		if found {
			return false
		}

		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case ast.Expr:
			if tv, ok := info.Types[n]; ok && (tv.IsType() || tv.Value != nil) {
				// a type, or a constant the compiler has folded
				return false
			}

			found = panics(n, info)
		}

		return !found
	})

	return found
}

// panics tells whether one expression, apart from its operands, can panic
// or block when it is evaluated.
func panics(e ast.Expr, info *types.Info) bool {
	switch n := e.(type) {
	case *ast.IndexExpr:
		return indexPanics(n, info)
	case *ast.IndexListExpr:
		// an instantiation of a generic function or type: no evaluation
		return false
	case *ast.SliceExpr:
		return slicePanics(n, info)
	case *ast.StarExpr, *ast.TypeAssertExpr:
		return true
	case *ast.BinaryExpr:
		return binaryPanics(n, info)
	case *ast.UnaryExpr:
		return n.Op == token.ARROW
	case *ast.SelectorExpr:
		return selectorPanics(n, info)
	case *ast.CallExpr:
		return callPanics(n, info)
	case *ast.CompositeLit:
		return mapKeyPanics(n, info)
	}

	return false
}

func indexPanics(n *ast.IndexExpr, info *types.Info) bool {
	tv, ok := info.Types[n.X]
	if !ok {
		return true
	}

	if _, generic := tv.Type.Underlying().(*types.Signature); generic {
		// an instantiation, F[int]
		return false
	}

	if tv.Value != nil {
		// a constant string at a constant index, which the compiler checks
		return !constant(n.Index, info)
	}

	switch tv.Type.Underlying().(type) {
	case *types.Map:
		return false
	case *types.Array:
		// a constant index into an array the compiler checks
		return !constant(n.Index, info)
	}

	return true
}

func slicePanics(n *ast.SliceExpr, info *types.Info) bool {
	tv, ok := info.Types[n.X]
	if !ok {
		return true
	}

	if _, array := tv.Type.Underlying().(*types.Array); !array && tv.Value == nil {
		// bounds into anything but an array or a constant string are
		// checked only when they run
		return true
	}

	for _, b := range []ast.Expr{n.Low, n.High, n.Max} {
		if b != nil && !constant(b, info) {
			return true
		}
	}

	return false
}

func binaryPanics(n *ast.BinaryExpr, info *types.Info) bool {
	switch n.Op {
	case token.QUO, token.REM:
		return integer(n.X, info) && !constant(n.Y, info)
	case token.SHL, token.SHR:
		return !constant(n.Y, info) && signed(n.Y, info)
	case token.EQL, token.NEQ:
		return iface(n.X, info) && !isNil(n.Y, info) || iface(n.Y, info) && !isNil(n.X, info)
	default:
		// arithmetic that wraps, logic, string concatenation and ordered
		// comparisons of basic values
		return false
	}
}

func selectorPanics(n *ast.SelectorExpr, info *types.Info) bool {
	sel, ok := info.Selections[n]
	if !ok {
		// a qualified name, pkg.Name
		return false
	}

	if sel.Indirect() {
		// through a pointer that may be nil
		return true
	}

	return sel.Kind() == types.MethodVal && types.IsInterface(sel.Recv())
}

func callPanics(n *ast.CallExpr, info *types.Info) bool {
	if tv, ok := info.Types[n.Fun]; ok && tv.IsType() {
		// a conversion: of a slice to an array or to an array pointer,
		// which panics when the slice is short
		if len(n.Args) != 1 {
			return false
		}

		from, ok := info.Types[n.Args[0]]
		if !ok {
			return true
		}

		if _, slice := from.Type.Underlying().(*types.Slice); !slice {
			return false
		}

		to := tv.Type.Underlying()
		if p, ok := to.(*types.Pointer); ok {
			to = p.Elem().Underlying()
		}

		_, array := to.(*types.Array)

		return array
	}

	var obj types.Object

	switch fun := ast.Unparen(n.Fun).(type) {
	case *ast.Ident:
		obj = info.Uses[fun]
	case *ast.SelectorExpr:
		obj = info.Uses[fun.Sel]
	}

	b, ok := obj.(*types.Builtin)
	if !ok {
		// a call: whether it acts is the pure calls' question
		return false
	}

	switch b.Name() {
	case "make":
		for _, a := range n.Args[1:] {
			if !constant(a, info) {
				return true
			}
		}

		return false
	case "Add", "Slice", "SliceData", "String", "StringData":
		// unsafe's, whose arguments it trusts
		return true
	}

	return false
}

// mapKeyPanics: a map literal whose key may hold a value that can't be
// hashed, given by an expression the compiler can't check.
func mapKeyPanics(n *ast.CompositeLit, info *types.Info) bool {
	tv, ok := info.Types[n]
	if !ok {
		return true
	}

	m, ok := tv.Type.Underlying().(*types.Map)
	if !ok || !holdsInterface(m.Key(), map[types.Type]bool{}) {
		return false
	}

	for _, el := range n.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if ok && !constant(kv.Key, info) {
			return true
		}
	}

	return false
}

// holdsInterface tells whether values of a type may hold an interface value,
// whose dynamic type decides whether it compares.
func holdsInterface(t types.Type, seen map[types.Type]bool) bool {
	if seen[t] {
		return false
	}

	seen[t] = true

	switch u := t.Underlying().(type) {
	case *types.Interface:
		return true
	case *types.Array:
		return holdsInterface(u.Elem(), seen)
	case *types.Struct:
		for f := range u.Fields() {
			if holdsInterface(f.Type(), seen) {
				return true
			}
		}
	}

	return false
}

func constant(e ast.Expr, info *types.Info) bool {
	tv, ok := info.Types[e]

	return ok && tv.Value != nil
}

func isNil(e ast.Expr, info *types.Info) bool {
	tv, ok := info.Types[e]

	return ok && tv.IsNil()
}

func integer(e ast.Expr, info *types.Info) bool {
	tv, ok := info.Types[e]
	if !ok {
		return true
	}

	b, ok := tv.Type.Underlying().(*types.Basic)

	return ok && b.Info()&types.IsInteger != 0
}

func signed(e ast.Expr, info *types.Info) bool {
	tv, ok := info.Types[e]
	if !ok {
		return true
	}

	b, ok := tv.Type.Underlying().(*types.Basic)

	return ok && b.Info()&types.IsUnsigned == 0
}

func iface(e ast.Expr, info *types.Info) bool {
	tv, ok := info.Types[e]

	return ok && types.IsInterface(tv.Type)
}
