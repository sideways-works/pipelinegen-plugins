package graph

import (
	"slices"
	"strings"
	"testing"
)

func without(n string) []string {
	return slices.DeleteFunc(slices.Clone(narrowed), func(m string) bool { return m == n })
}

func TestNarrowingsOffFallBackToTheWideRule(t *testing.T) {
	both := []string{cancel, create}

	cases := map[string]struct {
		edit         func(t *testing.T, root string)
		narrow, wide []string
	}{
		NarrowMethodSets: {
			// a method no slice ran: bound only by its type's method set
			edit: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "shop/orders/orders.go", "return o.Total.Cents", "return o.Total.Cents + 1")
			},
			narrow: nil, wide: both,
		},
		NarrowPureInitialisers: {
			edit: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "shop/money/money.go", `"EUR")`, `"CHF")`)
			},
			narrow: []string{create}, wide: both,
		},
		NarrowReferencedNew: {
			edit: func(t *testing.T, root string) {
				t.Helper()
				write(t, root, "shop/money/net.go", "package money\n\nfunc Net(a Amount) Amount { return a }\n")
				edit(t, root, "shop/orders/orders.go", "money.Hook()", "money.Hook()\n\t_ = money.Net(money.Amount{})")
			},
			narrow: []string{create}, wide: both,
		},
	}

	for n, c := range cases {
		t.Run(n, func(t *testing.T) {
			base, head := fixture(t), fixture(t)
			c.edit(t, head)

			if got := selectedWith(t, base, head, narrowed, shopSlices(), nil)[Executed]; !slices.Equal(got, c.narrow) {
				t.Errorf("on: selects %v, want %v", got, c.narrow)
			}

			if got := selectedWith(t, base, head, without(n), shopSlices(), nil)[Executed]; !slices.Equal(got, c.wide) {
				t.Errorf("off: selects %v, want %v", got, c.wide)
			}
		})
	}
}

func TestUnreachedNewSelectsNothing(t *testing.T) {
	base, head := fixture(t), fixture(t)
	write(t, head, "shop/money/extra.go", "package money\n\nfunc Extra() int { return 1 }\n")

	n := append(slices.Clone(narrowed), NarrowUnreachedNew)
	if got := selectedWith(t, base, head, n, shopSlices(), nil); len(got[Executed]) != 0 || len(got[Dependents]) != 0 {
		t.Errorf("selects %v, want nothing", got)
	}
}

func TestSetupSplit(t *testing.T) {
	// the batch's setup ran Gross; each slice ran only its own entry point
	ss := shopSlices()
	ss[0].List = []string{"go:shop/orders#func:Create"}
	ss[1].List = []string{"go:shop/orders#func:Cancel"}
	setup := map[string][]string{"backend": {"go:shop/money#func:Gross"}}

	base, head := fixture(t), fixture(t)
	edit(t, head, "shop/money/money.go", "_ = Currency\n", "_ = Currency + \"\"\n")

	split := append(slices.Clone(narrowed), NarrowSetupSplit)
	if got := selectedWith(t, base, head, split, ss, setup)[Executed]; !slices.Equal(got, []string{SetupSlice}) {
		t.Errorf("split: selects %v, want only the setup's binding", got)
	}

	if got := selectedWith(t, base, head, narrowed, ss, setup)[Executed]; !slices.Equal(got, []string{cancel, create}) {
		t.Errorf("no split: selects %v, want every slice", got)
	}
}

func TestDropped(t *testing.T) {
	root := fixture(t)

	on := append(slices.Clone(narrowed), NarrowUnreachedNew, NarrowInterfaceMethods)
	tree := load(t, root)
	allSets(t, tree)
	a := tree.Bind(Request{Slices: shopSlices(), Narrowings: on})

	for _, r := range a.Results {
		if r.Slice != cancel || r.Width != Executed {
			continue
		}

		d := r.Binding.Dropped
		if !slices.Contains(d[NarrowMethodSets], "go:shop/orders#method:Order.Price") {
			t.Errorf("method-sets dropped %v, want the method no slice ran", d[NarrowMethodSets])
		}

		if !slices.Contains(d[NarrowPureInitialisers], "go:shop/money#var:Currency") {
			t.Errorf("pure-initialisers dropped %v, want Currency", d[NarrowPureInitialisers])
		}

		if !slices.Contains(d[NarrowInterfaceMethods], "go:shop/orders#type:Order") {
			t.Errorf("interface-methods dropped %v, want the type whose method set no interface reaches", d[NarrowInterfaceMethods])
		}

		if !slices.Contains(d[NarrowUnreachedNew], "unbound:shop/money") {
			t.Errorf("unreached-new dropped %v", d[NarrowUnreachedNew])
		}

		return
	}

	t.Fatal("no executed binding for cancel")
}

func TestEveryNarrowingIsDeclared(t *testing.T) {
	levels := map[string]int{
		NarrowSetupSplit: 3, NarrowUnreachedNew: 3, NarrowMethodSets: 5,
		NarrowPureInitialisers: 5, NarrowPureFunctions: 5, NarrowPureTestVariables: 5, NarrowReferencedNew: 5, NarrowModuleBumps: 5,
		NarrowTestMethodSets: 5, NarrowInterfaceMethods: 4,
	}

	for n, level := range levels {
		if !slices.ContainsFunc(Narrowings, func(i Narrowing) bool { return i.Name == n && i.Note != "" && i.OffAt == level }) {
			t.Errorf("%s isn't declared with a note and off at %d", n, level)
		}
	}

	if len(Narrowings) != len(levels) {
		t.Errorf("%d narrowings declared, %d tested", len(Narrowings), len(levels))
	}
}

func TestPureTestVariables(t *testing.T) {
	base, head := fixture(t), fixture(t)
	for _, root := range []string{base, head} {
		write(t, root, "shop/orders/vars_test.go", "package orders\n\nimport \"regexp\"\n\nvar cents = regexp.MustCompile(`[0-9]+`)\n")
		edit(t, root, "shop/orders/create_test.go", "if o := Create(100); o.Total.Cents != 119 {",
			"if o := Create(100); o.Total.Cents != 119 || !cents.MatchString(\"1\") {")
	}

	edit(t, head, "shop/orders/vars_test.go", "`[0-9]+`", "`[0-9]*`")

	on := append(slices.Clone(narrowed), NarrowPureTestVariables)
	if got := selectedWith(t, base, head, on, shopSlices(), nil)[Executed]; !slices.Equal(got, []string{create}) {
		t.Errorf("on: selects %v, want the slice whose test uses it", got)
	}

	if got := selectedWith(t, base, head, narrowed, shopSlices(), nil)[Executed]; !slices.Equal(got, []string{cancel, create}) {
		t.Errorf("off: selects %v, want every slice of the package", got)
	}
}

func harness(t *testing.T, root, extra string) {
	t.Helper()
	write(t, root, "shop/orders/harness_test.go", "package orders\n\n"+
		"type env struct{ n int }\n\n"+
		"func newEnv() *env { return &env{} }\n\n"+
		"func (e *env) create() Order { return Create(100) }\n\n"+
		"func (e *env) cancel() Order { return Cancel(Order{}) }\n"+extra)
	edit(t, root, "shop/orders/create_test.go", "if o := Create(100); o.Total.Cents != 119 {", "if o := newEnv().create(); o.Total.Cents != 119 {")
	edit(t, root, "shop/orders/cancel_test.go", "if o := Cancel(Order{}); o.Total.Cents != 0 {", "f := newEnv().cancel\n\tif o := f(); o.Total.Cents != 0 {")
}

func TestTestMethodSets(t *testing.T) {
	both := []string{cancel, create}

	on := append(slices.Clone(narrowed), NarrowTestMethodSets)

	cases := map[string]struct {
		extra        string
		edit         func(t *testing.T, root string)
		narrow, wide []string
	}{
		"an added helper method selects nothing": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "shop/orders/harness_test.go", "type env struct", "func (e *env) extra() int { return e.n }\n\ntype env struct")
			},
			narrow: nil, wide: both,
		},
		"a changed helper body selects the tests calling it, a method value included": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "shop/orders/harness_test.go", "return Cancel(Order{})", "return Cancel(Order{Paid: 1})")
			},
			narrow: []string{cancel}, wide: both,
		},
		"a harness handed to an interface parameter binds every method": {
			extra: "\nfunc use(x interface{ create() Order }) { _ = x }\n\nvar _ = func() int { use(newEnv()); return 0 }()\n",
			edit: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "shop/orders/harness_test.go", "return Cancel(Order{})", "return Cancel(Order{Paid: 1})")
			},
			narrow: both, wide: both,
		},
		"a harness stored in an interface-typed field binds every method": {
			extra: "\ntype holder struct{ x any }\n\nvar held = holder{x: newEnv()}\n",
			edit: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "shop/orders/harness_test.go", "type env struct", "func (e *env) extra() int { return e.n }\n\ntype env struct")
			},
			narrow: both, wide: both,
		},
		"a harness embedded in a test type that stays put narrows": {
			extra: "\ntype wider struct{ *env }\n\nfunc newWider() wider { return wider{newEnv()} }\n",
			edit: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "shop/orders/harness_test.go", "type env struct", "func (e *env) extra() int { return e.n }\n\ntype env struct")
			},
			narrow: nil, wide: both,
		},
		"a harness embedded in a test type that escapes binds every method": {
			extra: "\ntype wider struct{ *env }\n\nvar heldWider any = wider{newEnv()}\n",
			edit: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "shop/orders/harness_test.go", "type env struct", "func (e *env) extra() int { return e.n }\n\ntype env struct")
			},
			narrow: both, wide: both,
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			base, head := fixture(t), fixture(t)
			harness(t, base, c.extra)
			harness(t, head, c.extra)
			c.edit(t, head)

			if got := selectedWith(t, base, head, on, shopSlices(), nil)[Executed]; !slices.Equal(got, c.narrow) {
				t.Errorf("on: selects %v, want %v", got, c.narrow)
			}

			if got := selectedWith(t, base, head, narrowed, shopSlices(), nil)[Executed]; !slices.Equal(got, c.wide) {
				t.Errorf("off: selects %v, want %v", got, c.wide)
			}
		})
	}
}

func TestEscapesSaysWhere(t *testing.T) {
	root := fixture(t)
	harness(t, root, "\ntype holder struct{ x any }\n\nvar held = holder{x: newEnv()}\n")

	at := load(t, root).Escapes()["go:shop/orders#type:env@test"]
	if !strings.Contains(at, "harness_test.go:") {
		t.Errorf("Escapes = %q, want the harness file's line", at)
	}
}

func TestUnboundNamesAtEveryLevel(t *testing.T) {
	root := fixture(t)
	write(t, root, "shop/money/net.go", "package money\n\nfunc Net(a Amount) Amount { return a }\n")
	edit(t, root, "shop/orders/orders.go", "money.Hook()", "money.Hook()\n\t_ = money.Net(money.Amount{})")

	tree := load(t, root)

	for _, c := range []struct {
		name       string
		narrowings []string
		dropped    map[string]bool
	}{
		{"both on", append(slices.Clone(narrowed), NarrowUnreachedNew), map[string]bool{NarrowUnreachedNew: true, NarrowReferencedNew: true}},
		{"referenced-new on", narrowed, map[string]bool{NarrowReferencedNew: true}},
		{"neither", without(NarrowReferencedNew), map[string]bool{}},
	} {
		a := tree.Bind(Request{Slices: shopSlices(), Narrowings: c.narrowings})

		names := a.Unbound[Executed]["shop/money"]
		if !slices.Contains(names, "go:shop/money#func:Unused") {
			t.Errorf("%s: unbound %v lacks Unused", c.name, names)
		}

		referenced := a.Narrowed[NarrowReferencedNew][Executed]["shop/money"]
		on := slices.Contains(c.narrowings, NarrowReferencedNew)

		switch {
		case on && (!slices.Equal(referenced, []string{"go:shop/money#func:Net"}) || slices.Contains(names, "go:shop/money#func:Net")):
			t.Errorf("%s: referenced %v, names %v: want Net counted as reached by referenced-new alone", c.name, referenced, names)
		case !on && (a.Narrowed != nil || !slices.Contains(names, "go:shop/money#func:Net")):
			t.Errorf("%s: narrowed %v, names %v: want Net unbound", c.name, a.Narrowed, names)
		}

		for _, r := range a.Results {
			if r.Slice != create {
				continue
			}

			if linked := len(r.Binding.Linked) > 0; linked == c.dropped[NarrowUnreachedNew] {
				t.Errorf("%s, %s: linked %v: want it empty exactly while unreached-new is on", c.name, r.Width, r.Binding.Linked)
			}

			for _, n := range []string{NarrowUnreachedNew, NarrowReferencedNew} {
				got := slices.Contains(r.Binding.Dropped[n], "unbound:shop/money")
				want := c.dropped[n] && (r.Width == Executed || n == NarrowUnreachedNew)

				if got != want {
					t.Errorf("%s, %s: %s dropped %v, want the linked group: %v", c.name, r.Width, n, r.Binding.Dropped[n], want)
				}
			}
		}
	}
}

func inForce(level int) []string {
	var out []string

	for _, n := range Narrowings {
		if n.OffAt > level {
			out = append(out, n.Name)
		}
	}

	return out
}

func droppedBy(t *testing.T, base, head string, narrowings, ns []string) [2][]string {
	t.Helper()

	invs := [2]inventory{listUnits(t, base), listUnits(t, head)}
	as := [2]Answer{
		load(t, base).Bind(Request{Slices: shopSlices(), Narrowings: narrowings}),
		load(t, head).Bind(Request{Slices: shopSlices(), Narrowings: narrowings}),
	}

	wide := func(a Answer, w Width, n, g string) string {
		return strings.Join(a.Unbound[w][g], ",") + "|" + strings.Join(a.Narrowed[n][w][g], ",")
	}

	var out [2][]string

	for i, r := range as[1].Results {
		changed := false

		for _, n := range ns {
			for _, id := range append(slices.Clone(as[0].Results[i].Binding.Dropped[n]), r.Binding.Dropped[n]...) {
				if g, ok := strings.CutPrefix(id, "unbound:"); ok {
					changed = changed || wide(as[0], r.Width, n, g) != wide(as[1], r.Width, n, g)

					continue
				}

				b, inB := invs[0].digests[id]
				h, inH := invs[1].digests[id]
				changed = changed || b != h || inB != inH
			}
		}

		if changed {
			out[r.Width] = append(out[r.Width], r.Slice)
		}
	}

	for w := range out {
		slices.Sort(out[w])
	}

	return out
}

func TestDroppedMatchesTheLevelsAbove(t *testing.T) {
	edits := map[string]func(t *testing.T, root string){
		"a new declaration nothing reaches": func(t *testing.T, root string) {
			t.Helper()
			write(t, root, "shop/money/extra.go", "package money\n\nfunc Extra() int { return 1 }\n")
		},
		"a new declaration a bound one references": func(t *testing.T, root string) {
			t.Helper()
			write(t, root, "shop/money/net.go", "package money\n\nfunc Net(a Amount) Amount { return a }\n")
			edit(t, root, "shop/orders/orders.go", "money.Hook()", "money.Hook()\n\t_ = money.Net(money.Amount{})")
		},
	}

	levels := []int{2, 3, 5}

	for name, e := range edits {
		base, head := fixture(t), fixture(t)
		e(t, head)

		selected := map[int][2][]string{}
		for _, l := range levels {
			selected[l] = selectedWith(t, base, head, inForce(l), shopSlices(), nil)
		}

		for i, n := range levels {
			for _, l := range levels[i+1:] {
				var between []string

				for _, nw := range Narrowings {
					if n < nw.OffAt && nw.OffAt <= l {
						between = append(between, nw.Name)
					}
				}

				got := droppedBy(t, base, head, inForce(n), between)

				for _, w := range Widths {
					// a slice the lower level selects anyway was left out of
					// nothing
					got := slices.DeleteFunc(got[w], func(s string) bool { return slices.Contains(selected[n][w], s) })

					want := slices.DeleteFunc(slices.Clone(selected[l][w]), func(s string) bool { return slices.Contains(selected[n][w], s) })
					if !slices.Equal(got, want) {
						t.Errorf("%s, %s, levels %d to %d: dropped by %v %v, want %v", name, w, n, l, between, got, want)
					}
				}
			}
		}
	}
}
