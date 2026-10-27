package graph

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/sideways-works/pipelinegen-sbom-go/goid"
)

// testEscapes notes the test types of a test file whose values may reach an
// interface, where a method can run without a call naming it: any
// interface-typed slot counts, a type parameter's included. A method value or
// a promoted call names its method and is no escape.
func (t *Tree) testEscapes(fset *token.FileSet, idx *goid.Index, w work) {
	e := &escaper{t: t, fset: fset, idx: idx, info: w.info, named: map[*ast.StructType]bool{}}

	for _, d := range w.file.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			var sig *types.Signature
			if fn, ok := w.info.Defs[d.Name].(*types.Func); ok {
				sig = fn.Signature()
			}

			if d.Body != nil {
				e.walk(d.Body, sig)
			}
		case *ast.GenDecl:
			e.typeSpecs(d)
			e.walk(d, nil)
		}
	}
}

// typeSpecs notes which test types each named test struct embeds; walk
// then skips those structs, so only other embeddings count as escapes.
func (e *escaper) typeSpecs(g *ast.GenDecl) {
	for _, spec := range g.Specs {
		ts, ok := spec.(*ast.TypeSpec)
		if !ok {
			continue
		}

		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			continue
		}

		outer, ok := e.testType(e.info.Defs[ts.Name])
		if !ok {
			continue
		}

		e.named[st] = true

		for _, f := range st.Fields.List {
			if len(f.Names) > 0 {
				continue
			}

			namedIn(e.typeOf(f.Type), map[types.Type]bool{}, func(tn *types.TypeName) {
				if inner, ok := e.testType(tn); ok {
					e.t.embeds[outer] = append(e.t.embeds[outer], inner)
				}
			})
		}
	}
}

func (e *escaper) testType(o types.Object) (int32, bool) {
	tn, ok := o.(*types.TypeName)
	if !ok {
		return 0, false
	}

	id, ok := e.idx.Object(e.fset, tn)
	if !ok {
		return 0, false
	}

	i, ok := e.t.byID[id]
	if !ok || !e.t.Decls[i].Test || id.Kind() != goid.Type {
		return 0, false
	}

	return i, true
}

func (t *Tree) promoteEscapes() {
	for changed := true; changed; {
		changed = false

		for outer, at := range t.escapes {
			for _, inner := range t.embeds[outer] {
				if _, seen := t.escapes[inner]; !seen {
					t.escapes[inner] = at + " (embedded in " + string(t.Decls[outer].ID) + ")"
					changed = true
				}
			}
		}
	}
}

type escaper struct {
	t    *Tree
	fset *token.FileSet
	idx  *goid.Index
	info *types.Info
	// named are the structs of named test types, whose embeddings
	// typeSpecs records instead of counting them as escapes.
	named map[*ast.StructType]bool
}

func (e *escaper) walk(n ast.Node, sig *types.Signature) {
	ast.Inspect(n, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			if s, ok := e.info.Types[n].Type.(*types.Signature); ok {
				e.walk(n.Body, s)
			}

			return false
		case *ast.ReturnStmt:
			e.returns(n, sig)
		case *ast.CallExpr:
			e.call(n)
		case *ast.AssignStmt:
			e.assign(n)
		case *ast.ValueSpec:
			e.valueSpec(n)
		case *ast.CompositeLit:
			e.composite(n)
		case *ast.SendStmt:
			if ch, ok := e.typeOf(n.Chan).Underlying().(*types.Chan); ok {
				e.into(ch.Elem(), n.Value)
			}
		case *ast.StructType:
			e.embeds(n)
		case *ast.InterfaceType:
			// an interface embedding another lists methods, not values
		}

		return true
	})
}

func (e *escaper) typeOf(x ast.Expr) types.Type {
	if tv, ok := e.info.Types[x]; ok && tv.Type != nil {
		return tv.Type
	}

	return types.Typ[types.Invalid]
}

func (e *escaper) into(slot types.Type, x ast.Expr) {
	if slot == nil || !types.IsInterface(slot) {
		return
	}

	e.markIn(e.typeOf(x), x)
}

func (e *escaper) markIn(t types.Type, at ast.Node) {
	namedIn(t, map[types.Type]bool{}, func(tn *types.TypeName) {
		id, ok := e.idx.Object(e.fset, tn)
		if !ok {
			return
		}

		if i, ok := e.t.byID[id]; ok && e.t.Decls[i].Test && id.Kind() == goid.Type {
			if _, seen := e.t.escapes[i]; !seen {
				e.t.escapes[i] = e.fset.Position(at.Pos()).String()
			}
		}
	})
}

func (e *escaper) call(c *ast.CallExpr) {
	fun := e.info.Types[c.Fun]
	if fun.IsType() {
		// a conversion: T(x)
		for _, a := range c.Args {
			e.into(fun.Type, a)
		}

		return
	}

	if fun.IsBuiltin() {
		e.builtin(c)

		return
	}

	sig, ok := fun.Type.Underlying().(*types.Signature)
	if !ok {
		return
	}

	params := sig.Params()

	for i, a := range c.Args {
		switch {
		case sig.Variadic() && i >= params.Len()-1 && !c.Ellipsis.IsValid():
			if s, ok := params.At(params.Len() - 1).Type().(*types.Slice); ok {
				e.into(s.Elem(), a)
			}
		case i < params.Len():
			e.into(params.At(i).Type(), a)
		}
	}
}

func (e *escaper) builtin(c *ast.CallExpr) {
	id, ok := ast.Unparen(c.Fun).(*ast.Ident)
	if !ok || id.Name != "append" || len(c.Args) < 2 {
		return
	}

	if s, ok := e.typeOf(c.Args[0]).Underlying().(*types.Slice); ok {
		for _, a := range c.Args[1:] {
			e.into(s.Elem(), a)
		}
	}
}

func (e *escaper) assign(a *ast.AssignStmt) {
	if len(a.Lhs) != len(a.Rhs) {
		return
	}

	for i := range a.Lhs {
		e.into(e.typeOf(a.Lhs[i]), a.Rhs[i])
	}
}

func (e *escaper) valueSpec(v *ast.ValueSpec) {
	if v.Type == nil || len(v.Values) != len(v.Names) {
		return
	}

	slot := e.typeOf(v.Type)
	for _, x := range v.Values {
		e.into(slot, x)
	}
}

func (e *escaper) returns(r *ast.ReturnStmt, sig *types.Signature) {
	if sig == nil || sig.Results().Len() != len(r.Results) {
		return
	}

	for i, x := range r.Results {
		e.into(sig.Results().At(i).Type(), x)
	}
}

func (e *escaper) composite(c *ast.CompositeLit) {
	switch u := e.typeOf(c).Underlying().(type) {
	case *types.Struct:
		for i, el := range c.Elts {
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				if k, ok := kv.Key.(*ast.Ident); ok {
					for f := range u.Fields() {
						if f.Name() == k.Name {
							e.into(f.Type(), kv.Value)
						}
					}
				}
			} else if i < u.NumFields() {
				e.into(u.Field(i).Type(), el)
			}
		}
	case *types.Slice:
		e.elements(c, u.Elem(), nil)
	case *types.Array:
		e.elements(c, u.Elem(), nil)
	case *types.Map:
		e.elements(c, u.Elem(), u.Key())
	}
}

func (e *escaper) elements(c *ast.CompositeLit, elem, key types.Type) {
	for _, el := range c.Elts {
		if kv, ok := el.(*ast.KeyValueExpr); ok {
			if key != nil {
				e.into(key, kv.Key)
			}

			e.into(elem, kv.Value)

			continue
		}

		e.into(elem, el)
	}
}

// embeds marks a test type embedded in a struct other than a named test
// type's: its methods are promoted into a method set the analysis doesn't
// follow.
func (e *escaper) embeds(s *ast.StructType) {
	if e.named[s] {
		return
	}

	for _, f := range s.Fields.List {
		if len(f.Names) == 0 {
			e.markIn(e.typeOf(f.Type), f)
		}
	}
}

// Escapes lists the test types whose values reach an interface, each with
// where it was first seen: what keeps test-method-sets from narrowing them.
func (t *Tree) Escapes() map[string]string {
	out := make(map[string]string, len(t.escapes))
	for i, at := range t.escapes {
		out[string(t.Decls[i].ID)] = at
	}

	return out
}
