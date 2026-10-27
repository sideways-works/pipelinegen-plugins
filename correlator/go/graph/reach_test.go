package graph

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

// shopOutside is a module with two packages from outside the tree: tpl
// parses template text from an fs.FS its caller hands in and executes it
// on the caller's data, which the scan can't read; wrap hands what it gets
// on to tpl.
var shopOutside = map[string]string{
	"press/vendor/modules.txt": "# example.com/tpl v1.0.0\n## explicit; go 1.24\nexample.com/tpl\nexample.com/tpl/wrap\n",
	"press/vendor/example.com/tpl/tpl.go": `package tpl

import (
	"html/template"
	"io"
	"io/fs"
)

type Source struct {
	Pages fs.FS
	Extra any
}

func Render(w io.Writer, src Source, doc string, data any) error {
	b, err := fs.ReadFile(src.Pages, doc)
	if err != nil {
		return err
	}

	t, err := template.New(doc).Parse(string(b))
	if err != nil {
		return err
	}

	return t.Execute(w, data)
}

func Lazy[T any](w io.Writer, src Source, f func() T) error { return Render(w, src, "lazy.html", f()) }

func Size(b []byte) int { return len(b) }

var Hook func(v any)
`,
	"press/vendor/example.com/tpl/wrap/wrap.go": `package wrap

import (
	"io"

	"example.com/tpl"
)

func Wrap(w io.Writer, data any) error { return tpl.Render(w, tpl.Source{}, "wrap.html", data) }
`,
}

// press is the tree: a server never handed to the template package, an
// invoice handed to it, documents handed through an interface, a func whose
// result is handed over, and a value handed to a package that hands it on;
// pressAny adds a struct with an any field handed over.
var press = map[string]string{
	"press/go.mod": "module example.com/press\n\ngo 1.24\n\nrequire example.com/tpl v1.0.0\n",
	"press/srv/srv.go": `package srv

type Server struct{ n int }

func (s *Server) Refund() int { return s.n }
`,
	"press/money/money.go": `package money

type Money int

func (m Money) Cents() int { return int(m) }
`,
	"press/docs/docs.go": `package docs

type Doc interface{ Body() string }

type A struct{}

func (A) Body() string { return "a" }

type B struct{}

func (B) Body() string { return "b" }

type C struct{}

func (C) Title() string { return "c" }

type D struct{}

func (D) Title() string { return "d" }

// E has a Body, but not Doc's: it implements no interface handed over
type E struct{}

func (E) Body() int { return 0 }
`,
	"press/print/print.go": `package print

import (
	"io"

	"example.com/press/docs"
	"example.com/press/money"
	"example.com/press/srv"
	"example.com/tpl"
	"example.com/tpl/wrap"
)

type Invoice struct{ Total money.Money }

func Invoices(w io.Writer, s *srv.Server) error {
	_ = s.Refund()

	return tpl.Render(w, tpl.Source{}, "invoice.html", Invoice{})
}

func Docs(w io.Writer, d docs.Doc) error { return tpl.Render(w, tpl.Source{}, "doc.html", d) }

func Later(w io.Writer) error {
	return tpl.Lazy(w, tpl.Source{}, func() money.Money { return 1 })
}

func Wrapped(w io.Writer) error { return wrap.Wrap(w, docs.C{}) }
`,
	"press/hold/hold.go": `package hold

import (
	"example.com/press/docs"
	"example.com/press/srv"
)

func Hold(s *srv.Server) (*srv.Server, docs.D) { return s, docs.D{} }
`,
}

var pressAny = map[string]string{
	"press/loose/loose.go": `package loose

import (
	"io"

	"example.com/press/srv"
	"example.com/tpl"
)

type Bag struct{ Extra any }

func Any(w io.Writer, s *srv.Server) error {
	return tpl.Render(w, tpl.Source{}, "any.html", Bag{Extra: s})
}
`,
}

// pressFuncs hands values only into slots that can't hold a tree type:
// a []byte parameter, and a func value the tree made itself. pressHook
// calls a func value the template's package holds.
var (
	pressFuncs = map[string]string{
		"press/funcs/funcs.go": `package funcs

import (
	"example.com/press/srv"
	"example.com/tpl"
)

type Blob []byte

func (b Blob) Len() int { return len(b) }

func Use(s *srv.Server) int {
	f := func(v any) { _ = v }
	f(s)

	return tpl.Size(Blob("x"))
}
`,
	}
	pressHook = map[string]string{
		"press/hooked/hooked.go": `package hooked

import (
	"example.com/press/srv"
	"example.com/tpl"
)

func Use(s *srv.Server) { tpl.Hook(s) }
`,
	}
)

func pressRoot(t *testing.T, more ...map[string]string) string {
	t.Helper()

	root := t.TempDir()

	for _, files := range append([]map[string]string{press, shopOutside}, more...) {
		for rel, src := range files {
			write(t, root, rel, src)
		}
	}

	return root
}

func wholeFor(t *testing.T, root, dir string) []string {
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

	return out
}

func TestWhatIsHandedToATemplate(t *testing.T) {
	root := pressRoot(t)

	got := wholeFor(t, root, "press/print")
	want := []string{
		"go:press/docs#type:A", "go:press/docs#type:B", "go:press/docs#type:C", "go:press/docs#type:Doc",
		"go:press/money#type:Money", "go:press/print#type:Invoice",
	}

	if !slices.Equal(got, want) {
		t.Errorf("whole method sets %v, want %v", got, want)
	}

	if got := wholeFor(t, root, "press/hold"); len(got) != 0 {
		t.Errorf("a package that links no template binds %v whole", got)
	}

	if got := wholeFor(t, pressRoot(t, pressAny), "press/loose"); !slices.Contains(got, "go:press/srv#type:Server") ||
		!slices.Contains(got, "go:press/docs#type:D") {
		t.Errorf("with a struct with an any field handed over: %v, want every type", got)
	}

	// a binary that doesn't link the package handing the struct over owes
	// it nothing
	if got := wholeFor(t, pressRoot(t, pressAny), "press/print"); !slices.Equal(got, want) {
		t.Errorf("a package that doesn't link the one handing an any over binds %v whole, want %v", got, want)
	}
}

func pressSlices() []Slice {
	slice := func(name string, list ...string) Slice {
		return Slice{Suite: "backend", Name: name, HasList: true, List: list}
	}

	return []Slice{
		slice("invoices", "go:press/print#func:Invoices", "go:press/srv#method:Server.Refund"),
		slice("hold", "go:press/hold#func:Hold"),
	}
}

func TestOnlySlotsThatHoldTheTreesTypesHandThemOver(t *testing.T) {
	got := wholeFor(t, pressRoot(t, pressFuncs), "press/funcs")
	for _, id := range []string{"go:press/funcs#type:Blob", "go:press/srv#type:Server"} {
		if slices.Contains(got, id) {
			t.Errorf("%s bound whole: %v", id, got)
		}
	}

	if got := wholeFor(t, pressRoot(t, pressFuncs, pressHook), "press/hooked"); !slices.Contains(got, "go:press/srv#type:Server") {
		t.Errorf("a func value of the template's package called with the server: %v, want the server whole", got)
	}
}

func TestHandedTypesSelect(t *testing.T) {
	cases := map[string]struct {
		file, src string
		want      []string
	}{
		"money, which the invoice holds": {"press/money/due.go", "package money\n\nfunc (m Money) Due() bool { return m > 0 }\n", []string{"invoices"}},
		"the server, never handed over":  {"press/srv/start.go", "package srv\n\nfunc (s *Server) Start() int { return s.n }\n", nil},
		"a type nobody hands over":       {"press/docs/more.go", "package docs\n\nfunc (D) Head() string { return \"\" }\n", nil},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			base, head := pressRoot(t), pressRoot(t)
			write(t, head, c.file, c.src)

			if got := bothWays(t, base, head, atLevel2(), pressSlices()); !slices.Equal(got, c.want) {
				t.Errorf("selects %v, want %v", got, c.want)
			}
		})
	}
}

func TestAnAnyFieldSaysWhy(t *testing.T) {
	tree := load(t, pressRoot(t, pressAny))
	allSets(t, tree)

	forced := tree.Forced()
	if len(forced) != 1 || !strings.Contains(forced[0], "go:press/loose#type:Bag") {
		t.Errorf("Forced = %v, want the struct with an any field, once", forced)
	}
}

func TestJunctionsHoldOnlyWhileTheNarrowingIsOn(t *testing.T) {
	tree := load(t, pressRoot(t, pressAny))
	sets := allSets(t, tree)

	junction := func(id string) bool {
		return id == AllMethodsNode || strings.HasPrefix(id, "node:go:handed-") || strings.HasPrefix(id, "node:go:reach:") ||
			strings.HasPrefix(id, "node:go:has-method:")
	}

	var bare []LinkSet

	for _, s := range sets {
		kept := s
		kept.Links = nil

		for _, l := range s.Links {
			if junction(l.From) || junction(l.To) {
				if l.Narrowing != NarrowInterfaceMethods || l.While != WhileOn {
					t.Errorf("%s: %v holds with the narrowing off", s.Group, l)
				}

				continue
			}

			kept.Links = append(kept.Links, l)
		}

		bare = append(bare, kept)
	}

	off := atLevel2()
	delete(off, NarrowInterfaceMethods)

	for _, s := range pressSlices() {
		got, want := closure(sets, tree.roots(s), off), closure(bare, tree.roots(s), off)
		for w := range got {
			if !maps.Equal(got[w], want[w]) {
				t.Errorf("%s: the junctions change the closure with the narrowing off: %v", s.Name, diff(got[w], want[w]))
			}
		}

		if whole := tree.wholeSets(map[string]bool{"press/print": true, "press/loose": true}, nil, optionsOf(off)); len(whole) != 0 {
			t.Errorf("bind binds %v whole with the narrowing off", whole)
		}
	}
}

// handBase is a tree with no hand-offs of its own: a server, money and
// documents, beside the template package of shopOutside, which reads
// methods by a template text the scan can't read. Each case adds one
// package that hands something to it.
var handBase = map[string]string{
	"press/go.mod":         press["press/go.mod"],
	"press/srv/srv.go":     press["press/srv/srv.go"],
	"press/money/money.go": press["press/money/money.go"],
	"press/docs/docs.go":   press["press/docs/docs.go"],
	"press/vendor/example.com/tpl/all.go": `package tpl

import "io"

func RenderAll(w io.Writer, doc string, data ...any) error {
	for _, d := range data {
		if err := Render(w, Source{}, doc, d); err != nil {
			return err
		}
	}

	return nil
}
`,
}

func handRoot(t *testing.T, more ...map[string]string) string {
	t.Helper()

	root := t.TempDir()

	for _, files := range append([]map[string]string{handBase, shopOutside}, more...) {
		for rel, src := range files {
			write(t, root, rel, src)
		}
	}

	return root
}

// handTests are two packages whose binaries link the template package:
// one's test file hands it a value nobody can tell, the other's code and
// tests hand it nothing.
var handTests = map[string]string{
	"press/one/one.go": `package one

import (
	"example.com/press/docs"
	"example.com/press/srv"
	"example.com/tpl"
)

func Size() int { _, _ = docs.E{}, (*srv.Server)(nil); return tpl.Size(nil) }
`,
	"press/one/one_test.go": `package one

import (
	"io"
	"testing"

	"example.com/tpl"
)

func hand(w io.Writer, v any) error { return tpl.Render(w, tpl.Source{}, "x", v) }

func TestOne(t *testing.T) { _ = hand(io.Discard, Size()) }
`,
	"press/two/two.go": `package two

import (
	"example.com/press/docs"
	"example.com/press/srv"
	"example.com/tpl"
)

func Use(s *srv.Server) int { _ = docs.E{}; return tpl.Size(nil) + s.Refund() }
`,
	"press/two/two_test.go": `package two

import (
	"testing"

	"example.com/press/srv"
)

func TestUse(t *testing.T) { _ = Use(&srv.Server{}) }
`,
}

func wholeForTests(t *testing.T, root, dir string) []string {
	t.Helper()

	tree := load(t, root)
	allSets(t, tree)

	linked := map[string]bool{}
	testDirs := tree.link(linked, []string{dir}, Slice{Sources: []string{dir + "/x_test.go"}})

	var out []string
	for _, i := range tree.wholeSets(linked, testDirs, optionsOf(atLevel2())) {
		out = append(out, string(tree.Decls[i].ID))
	}

	slices.Sort(out)

	return out
}

func TestATestFileHandsOnlyInItsBinary(t *testing.T) {
	root := handRoot(t, handTests)

	if got := wholeForTests(t, root, "press/one"); !slices.Contains(got, "go:press/srv#type:Server") {
		t.Errorf("the test binary that hands an unknown value binds %v whole, want every type", got)
	}

	if got := wholeForTests(t, root, "press/two"); len(got) != 0 {
		t.Errorf("a test binary that hands nothing binds %v whole", got)
	}

	tree := load(t, root)
	allSets(t, tree)

	if forced := tree.Forced(); len(forced) != 1 || !strings.Contains(forced[0], "one_test.go:") {
		t.Errorf("Forced = %v, want the test file's place, once", forced)
	}
}

func TestTheNarrowingNeverSelectsMore(t *testing.T) {
	slice := func(name, src string, list ...string) Slice {
		return Slice{Suite: "backend", Name: name, HasList: true, Sources: []string{src}, List: list}
	}

	ss := []Slice{
		slice("one", "press/one/one_test.go", "go:press/one#func:Size"),
		slice("two", "press/two/two_test.go", "go:press/two#func:Use", "go:press/srv#method:Server.Refund"),
		slice("lit", "press/lit/lit_test.go", "go:press/lit#func:Lit"),
	}

	more := maps.Clone(handTests)
	more["press/lit/lit.go"] = `package lit

import (
	"io"

	"example.com/press/docs"
	"example.com/tpl"
)

func Lit(w io.Writer) error { return tpl.Render(w, tpl.Source{}, "x", []any{docs.A{}}) }
`
	more["press/lit/lit_test.go"] = "package lit\n\nimport (\n\t\"io\"\n\t\"testing\"\n)\n\nfunc TestLit(t *testing.T) { _ = Lit(io.Discard) }\n"

	on := atLevel2()
	off := maps.Clone(on)
	delete(off, NarrowInterfaceMethods)

	changes := map[string]struct {
		file, src string
		on        []string
	}{
		"a type nobody hands over":          {"press/docs/more.go", "package docs\n\nfunc (E) Head() string { return \"\" }\n", []string{"one"}},
		"the server, used but never handed": {"press/srv/start.go", "package srv\n\nfunc (s *Server) Start() int { return s.n }\n", []string{"one"}},
		"the document a package hands over": {"press/docs/more.go", "package docs\n\nfunc (A) Head() string { return \"\" }\n", []string{"lit", "one"}},
	}

	for name, c := range changes {
		t.Run(name, func(t *testing.T) {
			base, head := handRoot(t, more), handRoot(t, more)
			write(t, head, c.file, c.src)

			gotOn := bothWays(t, base, head, on, ss)
			gotOff := bothWays(t, base, head, off, ss)

			if !slices.Equal(gotOn, c.on) {
				t.Errorf("on selects %v, want %v", gotOn, c.on)
			}

			// one's test hands over a value nobody can tell: its own binary
			// binds every type whole, by design
			for _, s := range gotOn {
				if !slices.Contains(gotOff, s) && s != "one" {
					t.Errorf("on selects %s, off doesn't (%v against %v)", s, gotOn, gotOff)
				}
			}
		})
	}
}

// runOutside is a dependency that executes a template it is handed on the
// data it is handed, and parses none itself; z is a package of the tree
// that parses template text from a file, which the scan can't read.
var runOutside = map[string]string{
	"press/vendor/modules.txt": "# example.com/tpl v1.0.0\n## explicit; go 1.24\nexample.com/tpl\nexample.com/tpl/run\nexample.com/tpl/wrap\n",
	"press/vendor/example.com/tpl/run/run.go": `package run

import (
	"html/template"
	"io"
)

func Run(t *template.Template, w io.Writer, data any) error { return t.Execute(w, data) }
`,
	"press/z/z.go": `package z

import (
	"html/template"
	"os"
)

func Page() *template.Template {
	b, _ := os.ReadFile("page.html")

	return template.Must(template.New("page").Parse(string(b)))
}
`,
	"press/hand/hand.go": `package hand

import (
	"io"

	"example.com/press/docs"
	"example.com/press/z"
	"example.com/tpl/run"
)

func X(w io.Writer) error { return run.Run(z.Page(), w, docs.B{}) }
`,
}

func TestATemplateTheTreeParsesRunsWhatADependencyIsHanded(t *testing.T) {
	root := handRoot(t, runOutside)

	if got := wholeFor(t, root, "press/hand"); !slices.Contains(got, "go:press/docs#type:B") {
		t.Errorf("whole method sets %v, want the document handed over", got)
	}
}

// forwardOutside is a dependency with a printer interface whose own
// implementation renders template text the scan can't read, and a func
// that calls a printer it is given on values it is given.
var forwardOutside = map[string]string{
	"press/vendor/modules.txt": "# example.com/tpl v1.0.0\n## explicit; go 1.24\nexample.com/tpl\nexample.com/tpl/pr\nexample.com/tpl/wrap\n",
	"press/vendor/example.com/tpl/pr/pr.go": `package pr

import (
	"io"

	"example.com/tpl"
)

type Printer interface{ Print(w io.Writer, v any) error }

type real struct{}

func (real) Print(w io.Writer, v any) error { return tpl.Render(w, tpl.Source{}, "p", v) }

func New() Printer { return real{} }

func Each(p Printer, w io.Writer, vs ...any) error {
	for _, v := range vs {
		if err := p.Print(w, v); err != nil {
			return err
		}
	}

	return nil
}
`,
}

func gateSrc(file, more string) map[string]string {
	return map[string]string{
		"press/gate/gate.go": "package gate\n",
		"press/gate/" + file: `package gate

import (
	"io"

	"example.com/press/docs"
	"example.com/press/srv"
	"example.com/tpl/pr"
)

var _ = (*srv.Server)(nil)

type Gate struct{ pr.Printer }

func (g *Gate) Print(w io.Writer, v any) error { return g.Printer.Print(w, v) }

func Use(w io.Writer) error {
	var p pr.Printer = &Gate{pr.New()}

	return p.Print(w, docs.A{})
}
` + more,
	}
}

func TestAForwardedParameterHoldsWhatItsCallsPass(t *testing.T) {
	cases := map[string]struct {
		file, more string
		want       []string
		why        string
	}{
		"in a test file":            {"gate_test.go", "", []string{"go:press/docs#type:A"}, ""},
		"in the package's own code": {"print.go", "", []string{"go:press/docs#type:A"}, ""},
		"a direct call": {"print.go", `
func Direct(w io.Writer) error { return (&Gate{pr.New()}).Print(w, docs.C{}) }
`, []string{"go:press/docs#type:A", "go:press/docs#type:C"}, ""},
		"a method expression's call": {"print.go", `
func Expr(w io.Writer) error { return (*Gate).Print(&Gate{pr.New()}, w, docs.C{}) }
`, []string{"go:press/docs#type:A", "go:press/docs#type:C"}, ""},
		"a method expression's call passing a value nobody can tell": {"print.go", `
func ExprPass(w io.Writer, v any) error { return (*Gate).Print(&Gate{pr.New()}, w, v) }
`, nil, "a parameter of a function others can call"},
		"a direct call passing a value nobody can tell": {"print.go", `
func Pass(w io.Writer, v any) error { return (&Gate{pr.New()}).Print(w, v) }
`, nil, "a parameter of a function others can call"},
		"a dependency calling it with what it was handed": {"print.go", `
func Many(w io.Writer) error { return pr.Each(&Gate{pr.New()}, w, docs.B{}) }
`, []string{"go:press/docs#type:A", "go:press/docs#type:B", "go:press/gate#type:Gate"}, ""},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			root := handRoot(t, forwardOutside, gateSrc(c.file, c.more))

			got := wholeForTests(t, root, "press/gate")

			tree := load(t, root)
			allSets(t, tree)
			forced := tree.Forced()

			if c.why == "" {
				if !slices.Equal(got, c.want) || len(forced) != 0 {
					t.Errorf("whole method sets %v, Forced %v; want %v and nothing forced", got, forced, c.want)
				}

				return
			}

			if !slices.Contains(got, "go:press/srv#type:Server") {
				t.Errorf("whole method sets %v, want every type", got)
			}

			if len(forced) != 1 || !strings.Contains(forced[0], "print.go:") || !strings.Contains(forced[0], c.why) {
				t.Errorf("Forced = %v, want the call's place, once, with %q", forced, c.why)
			}
		})
	}
}
