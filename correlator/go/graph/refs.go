package graph

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen-sbom-go/goid"
)

func (t *Tree) fileRefs(fset *token.FileSet, idx *goid.Index, w work, calls map[*types.Func][]int32) {
	for _, d := range w.decls {
		self := t.byID[d.ID]
		r := &refs{t: t, fset: fset, idx: idx, self: self, seen: map[int32]bool{self: true}}

		ast.Inspect(d.node, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				r.ident(w.info, id, calls)
			}

			return true
		})
	}
}

type refs struct {
	t    *Tree
	fset *token.FileSet
	idx  *goid.Index
	self int32
	seen map[int32]bool
}

// ident notes what an identifier names: the declaration of the object it
// uses, the named types of a variable it uses or defines, and the
// interface methods it calls.
func (r *refs) ident(info *types.Info, id *ast.Ident, calls map[*types.Func][]int32) {
	if o := info.Uses[id]; o != nil {
		ti, ok := r.add(o)

		switch o := o.(type) {
		case *types.Var:
			r.typeNames(o.Type())
		case *types.Func:
			recv := o.Signature().Recv()

			switch {
			case recv != nil && types.IsInterface(recv.Type()):
				calls[o] = append(calls[o], r.self)
			case recv != nil && ok:
				// a method selection, a method value or a promoted call:
				// it binds the signature of the method it resolves to, so
				// a method that shadows it, or its removal, reaches it
				if sig, has := r.t.sigID(ti); has && !slices.Contains(r.t.sels[r.self], sig) {
					r.t.sels[r.self] = append(r.t.sels[r.self], sig)
				}
			}
		}
	}

	if v, ok := info.Defs[id].(*types.Var); ok {
		r.typeNames(v.Type())
	}
}

func (r *refs) typeNames(tt types.Type) {
	namedIn(tt, map[types.Type]bool{}, func(tn *types.TypeName) { r.add(tn) })
}

// sigID is the signature unit go-sbom lists for a method: one per method of
// a type outside the tests, named as the method is (a method a test file
// declares on a package type keeps its suffix).
func (t *Tree) sigID(m int32) (string, bool) {
	d := t.Decls[m]
	if d.ID.Kind() != goid.Method || d.Recv == "" {
		return "", false
	}

	ti, ok := t.byID[goid.New(d.ID.Dir(), goid.Type, d.Recv)]
	if !ok || t.Decls[ti].Test {
		return "", false
	}

	return string(goid.New(d.ID.Dir(), goid.MethodSig, d.ID.Name())), true
}

func (r *refs) add(o types.Object) (int32, bool) {
	if o == nil || o.Pkg() == nil || !o.Pos().IsValid() {
		return 0, false
	}

	if _, ok := r.t.mods.dirOf(o.Pkg().Path()); !ok {
		return 0, false
	}

	var (
		ti int32
		ok bool
	)

	if r.t.syntax[o.Pkg()] {
		var target goid.ID
		if target, ok = r.idx.Object(r.fset, o); ok {
			ti, ok = r.t.byID[target]
		}
	} else {
		ti, ok = r.t.externalID(o)
	}

	if ok && !r.seen[ti] {
		r.seen[ti] = true
		r.t.refs[r.self] = append(r.t.refs[r.self], ti)
	}

	return ti, ok
}

// namedIn calls fn for each named type a type is made of, without looking
// inside the named types themselves: their own definitions are
// declarations with references of their own.
func namedIn(t types.Type, seen map[types.Type]bool, fn func(*types.TypeName)) {
	if t == nil || seen[t] {
		return
	}

	seen[t] = true

	switch t := t.(type) {
	case *types.Alias:
		fn(t.Obj())
		namedIn(types.Unalias(t), seen, fn)
	case *types.Named:
		fn(t.Origin().Obj())

		for a := range t.TypeArgs().Types() {
			namedIn(a, seen, fn)
		}
	case *types.Pointer:
		namedIn(t.Elem(), seen, fn)
	case *types.Slice:
		namedIn(t.Elem(), seen, fn)
	case *types.Array:
		namedIn(t.Elem(), seen, fn)
	case *types.Chan:
		namedIn(t.Elem(), seen, fn)
	case *types.Map:
		namedIn(t.Key(), seen, fn)
		namedIn(t.Elem(), seen, fn)
	case *types.Signature:
		namedIn(t.Params(), seen, fn)
		namedIn(t.Results(), seen, fn)
	case *types.Tuple:
		for v := range t.Variables() {
			namedIn(v.Type(), seen, fn)
		}
	case *types.Struct:
		for f := range t.Fields() {
			namedIn(f.Type(), seen, fn)
		}
	}
}

// concrete is one of the tree's non-interface types with the names of
// its methods, so an interface is checked only against types that have
// every method name.
type concrete struct {
	tn    *types.TypeName
	names map[string]bool
}

func (t *Tree) concreteTypes() map[string][]*concrete {
	byName := map[string][]*concrete{}

	for _, p := range t.typePkgs {
		sc := p.Scope()
		for _, n := range sc.Names() {
			tn, ok := sc.Lookup(n).(*types.TypeName)
			if !ok || tn.IsAlias() || types.IsInterface(tn.Type()) {
				continue
			}

			ms := types.NewMethodSet(types.NewPointer(tn.Type()))
			if ms.Len() == 0 {
				continue
			}

			c := &concrete{tn: tn, names: make(map[string]bool, ms.Len())}
			for sel := range ms.Methods() {
				c.names[sel.Obj().Name()] = true
				byName[sel.Obj().Name()] = append(byName[sel.Obj().Name()], c)
			}
		}
	}

	return byName
}

// implementers are the types that implement an interface. A generic type
// can't be checked uninstantiated: having every method name is enough.
func implementers(byName map[string][]*concrete, iface *types.Interface) []*concrete {
	out := []*concrete{}

	for _, c := range byName[iface.Method(0).Name()] {
		if !hasNames(c.names, iface) {
			continue
		}

		g, isNamed := c.tn.Type().(*types.Named)
		if (!isNamed || g.TypeParams().Len() == 0) && !types.Implements(types.NewPointer(c.tn.Type()), iface) {
			continue
		}

		out = append(out, c)
	}

	return out
}

func (t *Tree) implementations(fset *token.FileSet, idx *goid.Index, calls map[*types.Func][]int32) {
	byName := t.concreteTypes()

	// the same interface is a different object in each variant of its
	// package the type checker saw: resolve each interface once, by where
	// it is declared and its methods
	cache := map[string][]*concrete{}

	t.calls, t.implKeys = map[int32][]string{}, map[int32][]string{}
	t.meetingPoints(fset, idx)

	for fn, callers := range calls {
		iface, ok := fn.Signature().Recv().Type().Underlying().(*types.Interface)
		if !ok || iface.NumMethods() == 0 {
			continue
		}

		for _, c := range callers {
			t.calls[c] = appendNew(appendNew(t.calls[c], implKey(fn.Name(), fn.Signature())), fn.Name())
		}

		key := ifaceKey(fset, fn.Signature().Recv().Type(), iface)

		impl, ok := cache[key]
		if !ok {
			impl = implementers(byName, iface)
			cache[key] = impl
		}

		var targets []int32

		for _, c := range impl {
			m, _, _ := types.LookupFieldOrMethod(types.NewPointer(c.tn.Type()), true, c.tn.Pkg(), fn.Name())
			if mf, ok := m.(*types.Func); ok {
				if id, ok := idx.Object(fset, mf); ok {
					if ti, ok := t.byID[id]; ok {
						targets = append(targets, ti)
					}
				}
			}
		}

		for _, c := range callers {
			t.impls[c] = append(t.impls[c], targets...)
		}
	}

	for i, ts := range t.impls {
		slices.Sort(ts)
		t.impls[i] = slices.Compact(ts)
	}
}

// meetingPoints notes, for each method of the tree's types outside the
// tests, where interface calls meet it: by name and signature shape, and
// for a generic type's method by name alone, since an uninstantiated
// method's signature can't be compared.
func (t *Tree) meetingPoints(fset *token.FileSet, idx *goid.Index) {
	for _, p := range t.typePkgs {
		sc := p.Scope()
		for _, n := range sc.Names() {
			tn, ok := sc.Lookup(n).(*types.TypeName)
			if !ok || tn.IsAlias() || types.IsInterface(tn.Type()) {
				continue
			}

			named, ok := tn.Type().(*types.Named)
			if !ok {
				continue
			}

			for m := range named.Methods() {
				id, ok := idx.Object(fset, m)
				if !ok {
					continue
				}

				ti, ok := t.byID[id]
				if !ok {
					continue
				}

				key := m.Name()
				if named.TypeParams().Len() == 0 {
					key = implKey(m.Name(), m.Signature())
				}

				t.implKeys[ti] = appendNew(t.implKeys[ti], key)
			}
		}
	}
}

func hasNames(names map[string]bool, iface *types.Interface) bool {
	for m := range iface.Methods() {
		if !names[m.Name()] {
			return false
		}
	}

	return true
}

// ifaceKey names an interface the same in every package variant.
func ifaceKey(fset *token.FileSet, recv types.Type, iface *types.Interface) string {
	var b strings.Builder

	if n, ok := types.Unalias(recv).(*types.Named); ok {
		b.WriteString(fset.Position(n.Obj().Pos()).String())
	}

	for m := range iface.Methods() {
		b.WriteString(" " + m.Name())
	}

	if b.Len() == 0 || !strings.Contains(b.String(), ":") {
		b.WriteString(" " + iface.String())
	}

	return b.String()
}
