package graph

import (
	"slices"
	"strings"
	"testing"
)

func handing(src string) map[string]string {
	return map[string]string{"press/hand/hand.go": `package hand

import (
	"io"
	"iter"

	"example.com/press/docs"
	"example.com/press/money"
	"example.com/press/srv"
	"example.com/tpl"
)

var (
	_ = docs.A{}
	_ = money.Money(0)
	_ = (*srv.Server)(nil)
	_ io.Writer
	_ iter.Seq[int]
	_ = tpl.Size
)

` + src}
}

func wholeAndForced(t *testing.T, root, dir string) ([]string, []string) {
	t.Helper()

	tree := load(t, root)
	allSets(t, tree)

	linked := map[string]bool{}
	tree.link(linked, []string{dir}, Slice{})

	var out []string
	for _, i := range tree.wholeSets(linked, nil, optionsOf(atLevel2())) {
		out = append(out, string(tree.Decls[i].ID))
	}

	slices.Sort(out)

	return out, tree.Forced()
}

func TestWhatAHandedInterfaceHolds(t *testing.T) {
	cases := map[string]struct {
		src  string
		want []string
	}{
		"a composite literal": {`
func X(w io.Writer) error { return tpl.Render(w, tpl.Source{}, "x", []any{docs.A{}, money.Money(1)}) }
`, []string{"go:press/docs#type:A", "go:press/money#type:Money"}},
		"a slice appended to": {`
func X(w io.Writer, n int) error {
	all := []any{docs.A{}}
	if n > 0 {
		all = append(all, money.Money(n))
	}

	return tpl.RenderAll(w, "x", all...)
}
`, []string{"go:press/docs#type:A", "go:press/money#type:Money"}},
		"a slice declared empty and filled in a loop": {`
func X(w io.Writer, n int) error {
	var all []any
	for i := range n {
		all = append(all, money.Money(i))
	}

	return tpl.RenderAll(w, "x", all...)
}
`, []string{"go:press/money#type:Money"}},
		"a map of slices ranged over": {`
func X(w io.Writer) error {
	byDoc := map[string][]any{}
	for _, d := range []docs.A{{}} {
		byDoc["a"] = append(byDoc["a"], d)
	}

	byDoc["m"] = append(byDoc["m"], money.Money(1))

	for doc, pages := range byDoc {
		if err := tpl.RenderAll(w, doc, pages...); err != nil {
			return err
		}
	}

	return nil
}
`, []string{"go:press/docs#type:A", "go:press/money#type:Money"}},
		"a map literal": {`
func X(w io.Writer, n int) error {
	return tpl.Render(w, tpl.Source{}, "x", map[string]any{"a": docs.A{}, "n": n})
}
`, []string{"go:press/docs#type:A"}},
		"a closure's parameter it hands on": {`
func X(w io.Writer, b *docs.B) error {
	part := func(doc string, v any) error { return tpl.Render(w, tpl.Source{}, doc, v) }
	if err := part("a", docs.A{}); err != nil {
		return err
	}

	return part("b", *b)
}
`, []string{"go:press/docs#type:A", "go:press/docs#type:B"}},
		"a closure called where it is written": {`
func X(w io.Writer) error {
	return func(v any) error { return tpl.Render(w, tpl.Source{}, "x", v) }(docs.D{})
}
`, []string{"go:press/docs#type:D"}},
		"an interface variable assigned on two paths": {`
func X(w io.Writer, n int) error {
	var v any = docs.A{}
	if n > 0 {
		v = docs.B{}
	}

	return tpl.Render(w, tpl.Source{}, "x", v)
}
`, []string{"go:press/docs#type:A", "go:press/docs#type:B"}},
		"a lookup in a local map": {`
func X(w io.Writer) error {
	m := map[string]any{"a": docs.A{}}
	v, ok := m["a"]
	if !ok {
		return nil
	}

	return tpl.Render(w, tpl.Source{}, "x", v)
}
`, []string{"go:press/docs#type:A"}},
		"what a method of a type handed over returns": {`
type Box struct{ n int }

func (b *Box) Parts() []any { return []any{&b.n, money.Money(1)} }

func X(w io.Writer) error { return tpl.Render(w, tpl.Source{}, "x", &Box{}) }
`, []string{"go:press/hand#type:Box", "go:press/money#type:Money"}},
		"an unexported package variable": {`
var pages = []any{docs.C{}, money.Money(1)}

func X(w io.Writer) error { return tpl.RenderAll(w, "x", pages...) }

func Count() int { return len(pages) }
`, []string{"go:press/docs#type:C", "go:press/money#type:Money"}},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, forced := wholeAndForced(t, handRoot(t, handing(c.src)), "press/hand")
			if !slices.Equal(got, c.want) {
				t.Errorf("whole method sets %v, want %v", got, c.want)
			}

			if len(forced) != 0 {
				t.Errorf("Forced = %v, want nothing", forced)
			}
		})
	}
}

func TestWhatAHandedInterfaceCantTell(t *testing.T) {
	cases := map[string]struct{ src, why string }{
		"a parameter of a function others can call": {`
func X(w io.Writer, v any) error { return tpl.Render(w, tpl.Source{}, "x", v) }
`, "a parameter of a function others can call"},
		"a field": {`
type Box struct{ V any }

func X(w io.Writer, b Box) error { return tpl.Render(w, tpl.Source{}, "x", b.V) }
`, "a field"},
		"a call's result": {`
func get() any { return docs.A{} }

func X(w io.Writer) error { return tpl.Render(w, tpl.Source{}, "x", get()) }
`, "a call's result"},
		"the second result of a call": {`
func two() (int, any) { return 0, docs.A{} }

func X(w io.Writer) error {
	_, v := two()

	return tpl.Render(w, tpl.Source{}, "x", v)
}
`, "a variable assigned from a call's results"},
		"a call's result a method of a type handed over returns": {`
type Box struct{ n int }

func get() any { return &srv.Server{} }

func (Box) Value() any { return get() }

func X(w io.Writer) error { return tpl.Render(w, tpl.Source{}, "x", Box{}) }
`, "the result of Value: a call's result"},
		"a value received from a channel": {`
func X(w io.Writer, ch chan any) error {
	v := <-ch

	return tpl.Render(w, tpl.Source{}, "x", v)
}
`, "a value received from a channel"},
		"a lookup in a map handed in": {`
func X(w io.Writer, m map[string]any) error { return tpl.Render(w, tpl.Source{}, "x", m["a"]) }
`, "a parameter of a function others can call"},
		"a slice whose address is taken": {`
func fill(all *[]any) { *all = append(*all, &srv.Server{}) }

func X(w io.Writer) error {
	all := []any{docs.A{}}
	fill(&all)

	return tpl.RenderAll(w, "x", all...)
}
`, "its address is taken"},
		"a slice handed to another call": {`
func fill(all []any) { all[0] = &srv.Server{} }

func X(w io.Writer) error {
	all := []any{docs.A{}}
	fill(all)

	return tpl.RenderAll(w, "x", all...)
}
`, "handed to a call, which may fill it"},
		"a closure used as a value": {`
func run(f func(any) error) error { return f(&srv.Server{}) }

func X(w io.Writer) error {
	part := func(v any) error { return tpl.Render(w, tpl.Source{}, "x", v) }

	return run(part)
}
`, "a parameter of a closure used as a value"},
		"an exported package variable": {`
var Pages = []any{docs.A{}}

func X(w io.Writer) error { return tpl.RenderAll(w, "x", Pages...) }
`, "an exported variable"},
		"a package variable another function fills": {`
var pages = []any{docs.A{}}

func Add(v any) { pages = append(pages, v) }

func X(w io.Writer) error { return tpl.RenderAll(w, "x", pages...) }
`, "a parameter of a function others can call"},
		"a slice also stored in a field": {`
type keep struct{ all []any }

func X(w io.Writer, k *keep) error {
	all := []any{docs.A{}}
	k.all = all

	return tpl.RenderAll(w, "x", all...)
}
`, "stored where the reading can't follow"},
		"a range over a func": {`
func X(w io.Writer) error {
	seq := iter.Seq[any](func(yield func(any) bool) { yield(docs.A{}) })
	for v := range seq {
		return tpl.Render(w, tpl.Source{}, "x", v)
	}

	return nil
}
`, "a range over no slice, array or map"},
		"a type switch's variable": {`
func X(w io.Writer) error {
	var x any = docs.A{}
	switch v := x.(type) {
	case docs.B:
		return nil
	default:
		return tpl.Render(w, tpl.Source{}, "x", v)
	}
}
`, "a variable whose declaration the reading can't follow"},
		"an element written through a range's value": {`
func get() any { return &srv.Server{} }

func X(w io.Writer) error {
	byDoc := map[string][]any{"a": {docs.A{}}}
	for _, pages := range byDoc {
		pages[0] = get()
	}

	return tpl.RenderAll(w, "x", byDoc["a"]...)
}
`, "a call's result"},
		"an element written through a slice of it": {`
func get() any { return &srv.Server{} }

func X(w io.Writer) error {
	all := []any{docs.A{}}
	head := all[:1]
	head[0] = get()

	return tpl.RenderAll(w, "x", all...)
}
`, "a call's result"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, forced := wholeAndForced(t, handRoot(t, handing(c.src)), "press/hand")
			if !slices.Contains(got, "go:press/srv#type:Server") || !slices.Contains(got, "go:press/docs#type:E") {
				t.Errorf("whole method sets %v, want every type's", got)
			}

			if len(forced) != 1 || !strings.Contains(forced[0], "hand.go:") || !strings.Contains(forced[0], c.why) {
				t.Errorf("Forced = %v, want the place, once, with %q", forced, c.why)
			}
		})
	}
}
