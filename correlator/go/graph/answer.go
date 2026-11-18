package graph

import (
	"slices"

	"github.com/sideways-works/pipelinegen-plugins/sbom/go/goid"
)

// binding turns an index binding into ids: whole packages as groups, the
// rest unit by unit, modules as dep:go units.
func (t *Tree) binding(b binding, dropped map[string][]string) Binding {
	out := Binding{Units: map[string]Reason{}, Linked: sortedKeys(b.linked), Dropped: dropped}

	byDir := map[string]int{}
	for i := range b.decls {
		byDir[t.Decls[i].ID.Dir()]++
	}

	whole := map[string]bool{}

	// a package all of whose declarations are bound is bound whole, unless
	// one of them stands for less than its unit (a type bound through its
	// declaration): binding it whole would bind its method set too
	narrowed := map[string]bool{}

	for i := range b.decls {
		if t.unitID(b, i) != string(t.Decls[i].ID) {
			narrowed[t.Decls[i].ID.Dir()] = true
		}
	}

	// a whole group binds every unit of the package, its tests' too: only a
	// package all of whose declarations are bound is one
	for _, dir := range out.Linked {
		p := t.pkgs[dir]
		if byDir[dir] == len(p.Decls)+len(p.TestDecls) && !narrowed[dir] {
			whole[dir] = true
			out.Whole = append(out.Whole, dir)
		}
	}

	for i, w := range b.decls {
		d := t.Decls[i]
		if !whole[d.ID.Dir()] {
			out.Units[t.unitID(b, i)] = t.reason(w, b)
		}
	}

	for m, w := range b.deps {
		out.Units[goid.Dep(m)] = t.reason(w, b)
	}

	for sig, w := range b.sigs {
		if !whole[goid.ID(sig).Dir()] {
			out.Units[sig] = t.reason(w, b)
		}
	}

	for id, w := range b.files {
		if !whole[goid.ID(id).Dir()] {
			out.Units[id] = t.reason(w, b)
		}
	}

	out.Vias = t.vias(b)

	return out
}

// vias lists, for each bound type, constant and variable, every bound
// declaration that references it: a type every slice binds through shared
// code is still a slice's own when its own code names it too.
func (t *Tree) vias(b binding) map[string][]string {
	out := map[string][]string{}

	for j := range b.decls {
		for _, r := range t.refs[j] {
			if _, ok := b.decls[r]; !ok {
				continue
			}

			switch t.Decls[r].ID.Kind() {
			case goid.Type, goid.Const, goid.Var:
				id := t.unitID(b, r)
				out[id] = append(out[id], t.unitID(b, j))
			case goid.Func, goid.Method, goid.Init, goid.Excluded, goid.TypeDecl, goid.MethodSig, goid.API:
			}
		}
	}

	for _, vs := range out {
		slices.Sort(vs)
	}

	return out
}

// unitID is the unit a bound declaration stands for: a type bound through its
// declaration stands for its typedecl unit, without the method set its full
// unit's digest holds.
func (t *Tree) unitID(b binding, i int32) string {
	d := t.Decls[i]
	o := b.opts

	if b.full[i] {
		return string(d.ID)
	}

	if _, escapes := t.escapes[i]; o.testMethodSets && o.methodSets && d.Test && d.ID.Kind() == goid.Type && len(t.methods[i]) > 0 && !escapes {
		return t.twin(i)
	}

	if o.ifaceMethods && t.hasDecl(i) {
		return t.twin(i)
	}

	return string(d.ID)
}

func (t *Tree) reason(w why, b binding) Reason {
	r := Reason{Kind: w.kind, Via: w.pkg}
	if w.via >= 0 {
		r.Via = t.unitID(b, w.via)
	}

	return r
}

// wideOptions are, for each narrowing in force that a binding can switch off,
// the options with it off and every other as it is.
func wideOptions(opts options, on map[string]bool) map[string]options {
	off := map[string]func(*options){
		NarrowMethodSets:        func(o *options) { o.methodSets = false },
		NarrowInterfaceMethods:  func(o *options) { o.ifaceMethods = false },
		NarrowPureInitialisers:  func(o *options) { o.pureInit = false },
		NarrowPureFunctions:     func(o *options) { o.pureFuncs = false },
		NarrowTestMethodSets:    func(o *options) { o.testMethodSets = false },
		NarrowPureTestVariables: func(o *options) { o.pureTestVars = false },
		NarrowModuleBumps:       func(o *options) { o.moduleBumps = false },
	}

	wide := map[string]options{}

	for n, set := range off {
		if on[n] {
			o := opts
			set(&o)
			wide[n] = o
		}
	}

	return wide
}

func (t *Tree) dropped(s Slice, narrow [2]binding, on map[string]bool, setup map[string][]string) [2]map[string][]string {
	out := [2]map[string][]string{{}, {}}
	opts := optionsOf(on)

	wide := wideOptions(opts, on)

	for _, n := range sortedKeys(wide) {
		w := t.bind(s, wide[n])
		for _, width := range Widths {
			out[width][n] = t.added(narrow[width], w[width])
		}
	}

	if on[NarrowSetupSplit] && s.Name != SetupSlice && len(setup[s.Suite]) > 0 {
		merged := s
		merged.List = append(slices.Clone(s.List), setup[s.Suite]...)

		w := t.bind(merged, opts)
		for _, width := range Widths {
			out[width][NarrowSetupSplit] = t.added(narrow[width], w[width])
		}
	}

	droppedGroups(narrow, on, out)

	return out
}

// droppedGroups adds the groups whose unbound declarations the narrowings for
// new declarations leave out; the two trees' unbound answers tell whether a
// change there would have selected the slice.
func droppedGroups(narrow [2]binding, on map[string]bool, out [2]map[string][]string) {
	for _, n := range []string{NarrowUnreachedNew, NarrowReferencedNew} {
		if !on[n] {
			continue
		}

		for _, width := range Widths {
			if n == NarrowReferencedNew && width != Executed {
				continue
			}

			for _, dir := range sortedKeys(narrow[width].linked) {
				out[width][n] = append(out[width][n], "unbound:"+dir)
			}
		}
	}
}

func (t *Tree) added(narrow, wide binding) []string {
	have := t.ids(narrow)

	var out []string

	for id := range t.ids(wide) {
		if !have[id] {
			out = append(out, id)
		}
	}

	slices.Sort(out)

	return out
}

// ids are the units a binding names: its declarations as the units they
// stand for, its signatures and its modules.
func (t *Tree) ids(b binding) map[string]bool {
	out := map[string]bool{}

	for i := range b.decls {
		out[t.unitID(b, i)] = true
	}

	for sig := range b.sigs {
		out[sig] = true
	}

	for m := range b.deps {
		out[goid.Dep(m)] = true
	}

	return out
}

// unbound lists, per width and package, the declarations no slice reaches:
// under the wide rule a new one selects every slice linking the package.
// Those referenced-new counts as reached are listed apart.
func (t *Tree) unbound(bound [2]map[int32]bool, referencedNew bool) (unbound, referenced [2]map[string][]string) {
	byRef := map[int32]bool{}

	if referencedNew {
		for k := range bound[Executed] {
			for _, r := range t.refs[k] {
				if !bound[Executed][r] {
					byRef[r] = true
				}
			}
		}
	}

	unbound = [2]map[string][]string{{}, {}}
	referenced = [2]map[string][]string{{}, {}}

	for _, dir := range sortedKeys(t.pkgs) {
		for _, w := range Widths {
			names, refs := []string{}, []string{}

			for _, i := range t.pkgs[dir].Decls {
				switch {
				case bound[w][i]:
				case w == Executed && byRef[i]:
					refs = append(refs, string(t.Decls[i].ID))
				default:
					names = append(names, string(t.Decls[i].ID))
				}
			}

			slices.Sort(names)
			slices.Sort(refs)

			unbound[w][dir] = names
			referenced[w][dir] = refs
		}
	}

	return unbound, referenced
}
