package graph

import (
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen-sbom-go/goid"
)

// problemFile is a package of constructors in the shape repositories write
// for their API errors: a struct built from the parameters, its address
// returned as an error.
const problemFile = `// Package problem holds the answers an API refuses with.
package problem

// Problem is a refusal with a status, a code and words.
type Problem struct {
	Status int
	Code   string
	Msg    string
}

func (p *Problem) Error() string { return p.Code + ": " + p.Msg }

// New is a problem with a status, a code and words.
func New(status int, code, msg string) error {
	return &Problem{Status: status, Code: code, Msg: msg}
}
`

// errorsFile builds the shop's problems through a one-line forward to the
// constructor in another package, with a constant of the standard library
// as an argument, and holds one variable for each shape the proof must
// refuse.
const errorsFile = `package money

import (
	"errors"
	"net/http"
	"strings"

	"example.com/shop/problem"
	"example.com/shop/whole"
)

// NewProblem forwards to the problem package.
func NewProblem(status int, code, msg string) error { return problem.New(status, code, msg) }

// ErrOver is used by Gross alone.
var ErrOver = NewProblem(http.StatusConflict, "over", "It is over.")

type pair struct{ a, b int }

type table map[string]int

func value(a, b int) pair          { return pair{a: a, b: b} }
func list(a int) []int             { return []int{0: a, 1: -a} }
func joined(a, b string) string    { return a + "-" + b }
func wrapped(msg string) error     { return errors.New(msg) }
func nested(code string) error     { return NewProblem(http.StatusGone, code, joined(code, "x")) }
func literalKey(v int) table       { return table{"a": v} }
func twoStatements(a int) pair     { b := a; return pair{a: b} }
func divides(a, b int) int         { return a / b }
func shifts(a, b int) int          { return a << b }
func compares(a, b any) bool       { return a == b }
func field(p *pair) int            { return p.a }
func converts(b []byte) string     { return string(b) }
func builtin(s []int) int          { return len(s) }
func computedKey(k string) table   { return table{k: 1} }
func generic[T any](v T) T         { return v }
func literal() func() int          { return func() int { return 1 } }
func acts(msg string) error        { return errors.Join(errors.New(msg)) }
func loopA(a int) int              { return loopB(a) }
func loopB(a int) int              { return loopA(a) }
func deref(p *pair) pair           { return *p }
func viaParam(f func() int) int    { return f() }
func lower(s string) string        { return strings.ToLower(s) }
func (p pair) sum() int            { return p.a + p.b }

var (
	PureValue      = value(1, 2)
	PureList       = list(3)
	PureJoined     = joined("a", "b")
	PureWrapped    = wrapped("w")
	PureNested     = nested("n")
	PureLiteralKey = literalKey(1)

	ActsTwo       = twoStatements(1)
	ActsDivides   = divides(4, 2)
	ActsShifts    = shifts(1, 2)
	ActsCompares  = compares(1, 2)
	ActsField     = field(&pair{})
	ActsConverts  = converts(nil)
	ActsBuiltin   = builtin(nil)
	ActsKey       = computedKey("k")
	ActsGeneric   = generic(1)
	ActsLiteral   = literal()
	ActsCalls     = acts("a")
	ActsLoop      = loopA(1)
	ActsDeref     = deref(&pair{})
	ActsViaParam  = viaParam(nil)
	ActsOutside   = lower("A")
	ActsMethod    = pair{}.sum()
	ActsWhole     = whole.Make(1)
)
`

func pureFixture(t *testing.T) string {
	t.Helper()

	root := fixture(t)
	write(t, root, "shop/problem/problem.go", problemFile)
	write(t, root, "shop/money/errors.go", errorsFile)
	// a package the analysis can't follow: its functions prove nothing
	write(t, root, "shop/whole/whole.go", "package whole\n\nimport _ \"unsafe\"\n\n//go:linkname rate example.com/shop/money.Rate\nvar rate int\n\nfunc Make(a int) int { return a }\n")
	edit(t, root, "shop/money/money.go", "_ = Currency\n", "_ = Currency\n\t_ = ErrOver\n")

	return root
}

func decl(t *testing.T, tree *Tree, dir string, kind goid.Kind, name string) *Decl {
	t.Helper()

	i, ok := tree.byID[goid.New(dir, kind, name)]
	if !ok {
		t.Fatalf("no %s %s in %s", kind, name, dir)
	}

	return tree.Decls[i]
}

func TestPureFunctionsProveConstructors(t *testing.T) {
	tree := load(t, pureFixture(t))

	for _, n := range []string{"ErrOver", "PureValue", "PureList", "PureJoined", "PureWrapped", "PureNested", "PureLiteralKey"} {
		if d := decl(t, tree, "shop/money", goid.Var, n); d.Init || !d.InitList || !d.InitAny {
			t.Errorf("%s: Init %v, InitList %v, InitAny %v; want a call only the proof knows pure", n, d.Init, d.InitList, d.InitAny)
		}
	}

	for _, n := range []string{
		"ActsTwo", "ActsDivides", "ActsShifts", "ActsCompares", "ActsField", "ActsConverts", "ActsBuiltin", "ActsKey",
		"ActsGeneric", "ActsLiteral", "ActsCalls", "ActsLoop", "ActsDeref", "ActsViaParam", "ActsOutside", "ActsMethod",
		"ActsWhole",
	} {
		if d := decl(t, tree, "shop/money", goid.Var, n); !d.Init {
			t.Errorf("%s: the proof took it as pure", n)
		}
	}
}

func TestPureFunctionsFromExportData(t *testing.T) {
	// money alone from source: problem is read from export data, and the
	// proof reads its syntax all the same
	root := pureFixture(t)

	tree, err := Load(t.Context(), root, Options{Packages: []string{"shop/money"}})
	if err != nil {
		t.Fatal(err)
	}

	if d := decl(t, tree, "shop/money", goid.Var, "ErrOver"); d.Init {
		t.Error("ErrOver acts when problem comes from export data")
	}

	sets := tree.LinkSets([]string{"shop/money"})
	if len(sets) != 1 || !slices.Contains(sets[0].Inputs, "shop/problem/problem.go") {
		t.Errorf("money's inputs %v, want the file that proved problem.New pure", sets[0].Inputs)
	}
}

func TestPureFunctionsLinks(t *testing.T) {
	tree := load(t, pureFixture(t))

	var links []Link

	for _, s := range tree.LinkSets([]string{"shop/money"}) {
		links = append(links, s.Links...)
	}

	from, to := GroupNode("shop/money"), "go:shop/money#var:ErrOver"
	got := slices.DeleteFunc(slices.Clone(links), func(l Link) bool { return l.From != from || l.To != to })

	want := []Link{
		{From: from, To: to, Kind: KindFollow, Narrowing: NarrowPureFunctions, While: WhileOff},
		{From: from, To: to, Kind: KindFollow, Narrowing: NarrowPureInitialisers, While: WhileOff},
	}

	slices.SortFunc(want, compareLinks)

	if !slices.Equal(got, want) {
		t.Errorf("links from the package to ErrOver %v, want %v", got, want)
	}
}

func withFunctions() []string { return append(slices.Clone(narrowed), NarrowPureFunctions) }

func TestPureFunctionsSelect(t *testing.T) {
	cases := map[string]struct {
		edit         func(t *testing.T, root string)
		narrow, wide []string
	}{
		"the variable's value": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "shop/money/errors.go", `"It is over."`, `"It has ended."`)
			},
			narrow: []string{create}, wide: []string{cancel, create},
		},
		"a constant of the standard library as its argument": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "shop/money/errors.go", "http.StatusConflict, \"over\"", "http.StatusGone, \"over\"")
			},
			narrow: []string{create}, wide: []string{cancel, create},
		},
		"a new variable a changed function uses": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "shop/money/errors.go", "// ErrOver is used", "// ErrGone is new.\nvar ErrGone = NewProblem(http.StatusGone, \"gone\", \"Gone.\")\n\n// ErrOver is used")
				edit(t, root, "shop/money/money.go", "_ = ErrOver\n", "_ = ErrOver\n\t_ = ErrGone\n")
			},
			narrow: []string{create}, wide: []string{cancel, create},
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			base, head := pureFixture(t), pureFixture(t)
			c.edit(t, head)

			if got := selectedWith(t, base, head, withFunctions(), shopSlices(), nil)[Executed]; !slices.Equal(got, c.narrow) {
				t.Errorf("on: selects %v, want %v", got, c.narrow)
			}

			if got := selectedWith(t, base, head, narrowed, shopSlices(), nil)[Executed]; !slices.Equal(got, c.wide) {
				t.Errorf("off: selects %v, want %v", got, c.wide)
			}
		})
	}
}

func TestPureFunctionsReachEveryReader(t *testing.T) {
	change := func(t *testing.T, root string) {
		t.Helper()
		edit(t, root, "shop/money/errors.go", `"It is over."`, `"It has ended."`)
	}

	cases := map[string]struct {
		setup func(t *testing.T, root string)
		want  []string
	}{
		"a function of another package": {
			setup: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "shop/orders/orders.go", "if money.Total(o) == 0 {", "_ = money.ErrOver\n\tif money.Total(o) == 0 {")
			},
			want: []string{cancel, create},
		},
		"a test file that reads it": {
			setup: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "shop/orders/cancel_test.go", "import \"testing\"", "import (\n\t\"testing\"\n\n\t\"example.com/shop/money\"\n)\n\nfunc over() error { return money.ErrOver }")
			},
			want: []string{cancel, create},
		},
		"an external test package": {
			setup: func(t *testing.T, root string) {
				t.Helper()
				write(t, root, "shop/orders/over_test.go", "package orders_test\n\nimport (\n\t\"testing\"\n\n\t\"example.com/shop/money\"\n)\n\nfunc TestOver(t *testing.T) { _ = money.ErrOver }\n")
			},
			want: []string{create, "shop/orders/over_test.go"},
		},
		"another variable's initialiser": {
			setup: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "shop/money/money.go", "func build()", "var Problems = []error{ErrOver}\n\nfunc build()")
				edit(t, root, "shop/orders/orders.go", "if money.Total(o) == 0 {", "_ = money.Problems\n\tif money.Total(o) == 0 {")
			},
			want: []string{cancel, create},
		},
		"an effectful initialiser that takes it": {
			setup: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "shop/money/money.go", "func build()", "var registered = register(ErrOver)\n\nfunc register(e error) int { println(e); return 1 }\n\nfunc build()")
			},
			want: []string{cancel, create},
		},
		"an init that sets it": {
			setup: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "shop/money/money.go", "func build()", "func init() { ErrOver = NewProblem(409, \"again\", ErrOver.Error()) }\n\nfunc build()")
			},
			want: []string{cancel, create},
		},
		"a go:linkname that pulls it": {
			setup: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "shop/orders/orders.go", "import \"example.com/shop/money\"", "import (\n\t_ \"unsafe\"\n\n\t\"example.com/shop/money\"\n)\n\n//go:linkname over example.com/shop/money.ErrOver\nvar over error")
			},
			want: []string{cancel, create},
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			base, head := pureFixture(t), pureFixture(t)
			c.setup(t, base)
			c.setup(t, head)
			change(t, head)

			ss := shopSlices()
			if name == "an external test package" {
				ss = append(ss, Slice{Suite: "backend", Name: "shop/orders/over_test.go", HasList: true, Sources: []string{"shop/orders/over_test.go"}})
			}

			got := selectedWith(t, base, head, withFunctions(), ss, nil)[Executed]

			slices.Sort(c.want)

			if !slices.Equal(got, c.want) {
				t.Errorf("selects %v, want %v", got, c.want)
			}
		})
	}
}

func TestPurityChangesBetweenTrees(t *testing.T) {
	impure := func(t *testing.T, root string) {
		t.Helper()
		edit(t, root, "shop/problem/problem.go", "\treturn &Problem{", "\tprintln(code)\n\n\treturn &Problem{")
	}

	ran := func() []Slice {
		ss := shopSlices()
		for i := range ss {
			ss[i].List = append(ss[i].List, "go:shop/money#func:NewProblem", "go:shop/problem#func:New")
		}

		return ss
	}

	for name, which := range map[string]int{"pure in the base, acting in the head": 1, "acting in the base, pure in the head": 0} {
		t.Run(name, func(t *testing.T) {
			roots := [2]string{pureFixture(t), pureFixture(t)}
			impure(t, roots[which])

			for i, root := range roots {
				tree := load(t, root)
				if d := decl(t, tree, "shop/money", goid.Var, "ErrOver"); d.Init != (i == which) {
					t.Errorf("tree %d: ErrOver acts %v", i, d.Init)
				}

				sets := tree.LinkSets([]string{"shop/money"})
				if !slices.Contains(sets[0].Inputs, "shop/problem/problem.go") {
					t.Errorf("tree %d: money's inputs %v lack the constructor's file", i, sets[0].Inputs)
				}
			}

			if got := selectedWith(t, roots[0], roots[1], withFunctions(), ran(), nil)[Executed]; !slices.Equal(got, []string{cancel, create}) {
				t.Errorf("selects %v, want every slice that ran the constructor", got)
			}
		})
	}

	t.Run("a pure call the config stops listing", func(t *testing.T) {
		root := fixture(t)
		write(t, root, "shop/money/listed.go", "package money\n\nimport \"strings\"\n\nvar Lower = strings.ToLower(\"A\")\n")

		listed, err := Load(t.Context(), root, Options{PureCalls: []string{"strings.ToLower"}})
		if err != nil {
			t.Fatal(err)
		}

		if d := decl(t, listed, "shop/money", goid.Var, "Lower"); d.Init || d.InitList {
			t.Error("a listed call acts")
		}

		if d := decl(t, load(t, root), "shop/money", goid.Var, "Lower"); !d.Init || !d.InitList {
			t.Error("a call the config doesn't list is pure")
		}
	})
}

func TestLinknamePullLinksTheTarget(t *testing.T) {
	root := pureFixture(t)
	edit(t, root, "shop/orders/orders.go", "import \"example.com/shop/money\"", "import (\n\t_ \"unsafe\"\n\n\t\"example.com/shop/money\"\n)\n\n//go:linkname over example.com/shop/money.ErrOver\nvar over error")

	want := Link{From: GroupNode("shop/orders"), To: "go:shop/money#var:ErrOver", Kind: KindFollow}

	for _, s := range load(t, root).LinkSets([]string{"shop/orders"}) {
		if slices.Contains(s.Links, want) {
			return
		}
	}

	t.Errorf("no link %v", want)
}

func TestPureInputsDontDependOnTheOrder(t *testing.T) {
	// two packages call the same forward; each one's inputs hold the files
	// of the functions its proof went through, in the other packages
	root := pureFixture(t)
	write(t, root, "shop/orders/errors.go", "package orders\n\nimport \"example.com/shop/money\"\n\nvar ErrLate = money.NewProblem(410, \"late\", \"Too late.\")\n")

	full := load(t, root)

	for _, dir := range []string{"shop/money", "shop/orders"} {
		sets := full.LinkSets([]string{dir})
		if !slices.Contains(sets[0].Inputs, "shop/problem/problem.go") {
			t.Errorf("%s: inputs %v lack the inner constructor's file", dir, sets[0].Inputs)
		}

		alone, err := Load(t.Context(), root, Options{Packages: []string{dir}})
		if err != nil {
			t.Fatal(err)
		}

		got := alone.LinkSets([]string{dir})[0]
		if !slices.Equal(got.InputUnits, sets[0].InputUnits) || !slices.Equal(got.Inputs, sets[0].Inputs) {
			t.Errorf("%s: inputs %v and %v loaded alone, %v and %v with every package", dir, got.Inputs, got.InputUnits, sets[0].Inputs, sets[0].InputUnits)
		}
	}
}

func TestAnsweringAFileAnswersItsReaders(t *testing.T) {
	root := pureFixture(t)
	write(t, root, "shop/orders/errors.go", "package orders\n\nimport \"example.com/shop/money\"\n\nvar ErrLate = money.NewProblem(410, \"late\", \"Too late.\")\n")

	readers, err := Readers(root, Options{}, []string{"shop/problem/problem.go"})
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(readers, []string{"shop/money", "shop/orders"}) {
		t.Errorf("readers of problem.go %v, want money and orders", readers)
	}

	readers, err = Readers(root, Options{}, []string{"shop/orders/orders.go"})
	if err != nil || len(readers) != 0 {
		t.Errorf("readers of orders.go %v (%v), want none: no proof reads it", readers, err)
	}

	// a file gone from the problem package: a proof that read a file beside
	// it may have read what it declared
	readers, err = Readers(root, Options{}, []string{"shop/problem/gone.go"})
	if err != nil || !slices.Equal(readers, []string{"shop/money", "shop/orders"}) {
		t.Errorf("readers of a gone file beside problem.go %v (%v), want money and orders", readers, err)
	}

	for _, s := range load(t, root).LinkSets(nil) {
		for _, u := range s.InputUnits {
			if !strings.Contains(u, "#api:") {
				t.Errorf("%s names %s as an input unit", s.Group, u)
			}
		}
	}
}

func TestANewConstructorNoRecordingRanSelectsItsPackage(t *testing.T) {
	registry := func(t *testing.T, root string) {
		t.Helper()
		edit(t, root, "shop/problem/problem.go", "// New is", "var registry = map[string]func(int, string, string) error{\"new\": New}\n\n// Lookup finds a constructor by name.\nfunc Lookup(name string) func(int, string, string) error { return registry[name] }\n\n// New is")
	}

	base, head := pureFixture(t), pureFixture(t)
	registry(t, base)
	registry(t, head)
	edit(t, head, "shop/problem/problem.go", "{\"new\": New}", "{\"new\": New, \"gone\": Gone}\n\n// Gone is a problem for what is gone.\nfunc Gone(status int, code, msg string) error {\n\treturn &Problem{Status: status, Code: code, Msg: msg}\n}")
	write(t, head, "shop/extra/extra.go", "package extra\n\nimport \"example.com/shop/problem\"\n\n// ErrGone is used by nothing yet.\nvar ErrGone = problem.Gone(410, \"gone\", \"Gone.\")\n")

	if got := selectedWith(t, base, head, withFunctions(), shopSlices(), nil)[Executed]; !slices.Equal(got, []string{cancel, create}) {
		t.Errorf("selects %v, want every slice linking problem", got)
	}
}

func TestAProofAcrossModules(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.work", "go 1.24\n\nuse (\n\t./mods/a\n\t./mods/b\n)\n")
	write(t, root, "mods/a/go.mod", "module example.com/a\n\ngo 1.24\n")
	write(t, root, "mods/a/a.go", "package a\n\nimport \"example.com/b\"\n\nvar Err = b.New(\"a\")\n")
	write(t, root, "mods/b/go.mod", "module example.com/b\n\ngo 1.24\n")
	write(t, root, "mods/b/b.go", "package b\n\ntype P struct{ C string }\n\nfunc (p *P) Error() string { return p.C }\n\nfunc New(c string) error { return &P{C: c} }\n")

	tree := load(t, root)
	if d := decl(t, tree, "mods/a", goid.Var, "Err"); d.Init {
		t.Error("a pure constructor of another module acts")
	}

	sets := tree.LinkSets([]string{"mods/a"})
	for _, in := range []string{"mods/b/b.go", "mods/b/go.mod"} {
		if !slices.Contains(sets[0].Inputs, in) {
			t.Errorf("a's inputs %v lack %s", sets[0].Inputs, in)
		}
	}

	if readers, err := Readers(root, Options{}, []string{"mods/b/go.mod"}); err != nil || !slices.Contains(readers, "mods/a") {
		t.Errorf("readers of b's go.mod %v (%v), want a", readers, err)
	}

	// the same path built from elsewhere, whose constructor acts: the
	// module path names alt/b first, the replace builds mods/b
	replaced := t.TempDir()
	write(t, replaced, "mods/a/go.mod", "module example.com/a\n\ngo 1.24\n\nrequire example.com/b v0.0.0\n\nreplace example.com/b => ../b\n")
	write(t, replaced, "mods/a/a.go", "package a\n\nimport \"example.com/b\"\n\nvar Err = b.New(\"a\")\n")
	write(t, replaced, "alt/b/go.mod", "module example.com/b\n\ngo 1.24\n")
	write(t, replaced, "alt/b/b.go", "package b\n\ntype P struct{ C string }\n\nfunc (p *P) Error() string { return p.C }\n\nfunc New(c string) error { return &P{C: c} }\n")
	write(t, replaced, "mods/b/go.mod", "module example.com/b\n\ngo 1.24\n")
	write(t, replaced, "mods/b/b.go", "package b\n\ntype P struct{ C string }\n\nfunc (p *P) Error() string { return p.C }\n\nfunc New(c string) error {\n\tprintln(c)\n\n\treturn &P{C: c}\n}\n")

	tree, err := Load(t.Context(), replaced, Options{Modules: []string{"mods/a"}})
	if err != nil {
		t.Fatal(err)
	}

	if d := decl(t, tree, "mods/a", goid.Var, "Err"); !d.Init {
		t.Error("a constructor built from a replace's directory was proved pure from another")
	}
}
