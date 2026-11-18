package goid

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"slices"
	"strings"
	"testing"
)

const shop = `package shop

type (
	Order struct{ Total int }
	List[T any] struct{ items []T }
)

const Max, Min = 10, 1

const (
	Cash = iota
	Card
)

var _ = register()

var _ = register()

var a, b = 1, 2

func init() {}

func init() {}

func (o *Order) Pay() int { return o.Total }

func (l *List[T]) Push(v T) { l.items = append(l.items, v) }

func register() int { f := func() int { return Max }; return f() }

func Sum[T int | float64](xs ...T) T { var s T; for _, x := range xs { s += x }; return s }

var total = Sum(1, 2)
`

func parse(t *testing.T, fset *token.FileSet, name, src string) *ast.File {
	t.Helper()

	f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}

	return f
}

func ids(ds []Decl) []ID {
	out := make([]ID, len(ds))
	for i, d := range ds {
		out[i] = d.ID
	}

	return out
}

func TestFile(t *testing.T) {
	fset := token.NewFileSet()
	got := ids(File("internal/shop", "shop.go", parse(t, fset, "/r/internal/shop/shop.go", shop)))

	want := []ID{
		"go:internal/shop#type:Order",
		"go:internal/shop#type:List",
		"go:internal/shop#const:Max,Min",
		"go:internal/shop#const:Cash",
		"go:internal/shop#const:Card",
		"go:internal/shop#var:_:shop.go:1",
		"go:internal/shop#var:_:shop.go:2",
		"go:internal/shop#var:a,b",
		"go:internal/shop#init:shop.go:1",
		"go:internal/shop#init:shop.go:2",
		"go:internal/shop#method:Order.Pay",
		"go:internal/shop#method:List.Push",
		"go:internal/shop#func:register",
		"go:internal/shop#func:Sum",
		"go:internal/shop#var:total",
	}
	if !slices.Equal(got, want) {
		t.Errorf("File =\n%v\nwant\n%v", got, want)
	}
}

func TestTestFiles(t *testing.T) {
	fset := token.NewFileSet()
	src := "package shop\n\nfunc helper() {}\n\nfunc (o *Order) fake() {}\n"
	xsrc := "package shop_test\n\nfunc helper() {}\n"

	got := ids(File(".", "shop_test.go", parse(t, fset, "/r/shop_test.go", src)))
	if want := []ID{"go:.#func:helper@test", "go:.#method:Order.fake@test"}; !slices.Equal(got, want) {
		t.Errorf("in-package test file: %v, want %v", got, want)
	}

	got = ids(File(".", "x_test.go", parse(t, fset, "/r/x_test.go", xsrc)))
	if want := []ID{"go:.#func:helper@xtest"}; !slices.Equal(got, want) {
		t.Errorf("external test package: %v, want %v", got, want)
	}

	if !got[0].Test() || ID("go:.#func:helper").Test() {
		t.Error("Test() is wrong")
	}
}

func TestParse(t *testing.T) {
	dir, kind, name, err := Parse("go:internal/shop#method:Order.Pay")
	if err != nil || dir != "internal/shop" || kind != Method || name != "Order.Pay" {
		t.Errorf("Parse = %q %q %q %v", dir, kind, name, err)
	}

	id := ID("go:.#init:shop.go:2")
	if id.Dir() != "." || id.Kind() != Init || id.Name() != "shop.go:2" {
		t.Errorf("%s: %q %q %q", id, id.Dir(), id.Kind(), id.Name())
	}

	if id := ID("go:internal/shop#api:shop"); id.Kind() != API || id.Name() != "shop" {
		t.Errorf("%s: %q %q", id, id.Kind(), id.Name())
	}

	for _, bad := range []string{"internal/shop#func:A", "go:#func:A", "go:x#func:", "go:x#lambda:A", "go:x", "dep:go:x"} {
		if _, _, _, err := Parse(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestIndexMapsTypeCheckerObjects(t *testing.T) {
	fset := token.NewFileSet()
	f := parse(t, fset, "/r/internal/shop/shop.go", shop)

	info := &types.Info{Uses: map[*ast.Ident]types.Object{}, Defs: map[*ast.Ident]types.Object{}, Instances: map[*ast.Ident]types.Instance{}}
	if _, err := (&types.Config{}).Check("example.com/shop", fset, []*ast.File{f}, info); err != nil {
		t.Fatal(err)
	}

	x := NewIndex()
	x.Add(fset, File("internal/shop", "shop.go", f))

	want := map[string]ID{
		"Max":   "go:internal/shop#const:Max,Min",
		"Total": "go:internal/shop#type:Order",
		"items": "go:internal/shop#type:List",
		"Sum":   "go:internal/shop#func:Sum",
		"f":     "go:internal/shop#func:register",
	}

	for ident, obj := range info.Uses {
		w, ok := want[ident.Name]
		if !ok {
			continue
		}

		if got, ok := x.Object(fset, obj); !ok || got != w {
			t.Errorf("%s at %s: %s, want %s", ident.Name, fset.Position(ident.Pos()), got, w)
		}
	}
}

func TestFileSkipsImports(t *testing.T) {
	fset := token.NewFileSet()
	src := "package shop\n\nimport \"fmt\"\n\nimport (\n\t\"os\"\n)\n\nvar _ = fmt.Sprint(os.Args)\n"

	if got := ids(File(".", "shop.go", parse(t, fset, "/r/shop.go", src))); !slices.Equal(got, []ID{"go:.#var:_:shop.go:1"}) {
		t.Errorf("File = %v, want the blank variable alone", got)
	}
}

func TestDep(t *testing.T) {
	if got := Dep("github.com/jackc/pgx/v5"); got != "dep:go:github.com/jackc/pgx/v5" {
		t.Errorf("Dep = %q", got)
	}

	if p, ok := DepPath("dep:go:golang.org/x/text"); !ok || p != "golang.org/x/text" {
		t.Errorf("DepPath = %q %v", p, ok)
	}

	for _, id := range []string{GoVersion, Toolchain, BazelSDK, "go:a#func:F", "dep:npm:react"} {
		if _, ok := DepPath(id); ok {
			t.Errorf("%s counts as a module", id)
		}
	}

	if _, kind, name, err := Parse("go:a#file:a_windows.go"); err != nil || kind != Excluded || name != "a_windows.go" {
		t.Errorf("Parse of a left-out file = %q %q %v", kind, name, err)
	}
}

func TestLabel(t *testing.T) {
	for id, want := range map[ID]string{
		"go:internal/notes#func:Create":           "notes.Create",
		"go:internal/notes#method:Store.Get":      "notes.Store.Get",
		"go:internal/notes#init:notes.go:1":       "package notes init (notes.go:1)",
		"go:internal/notes#type:env@test":         "notes.env (test)",
		"go:internal/notes#func:TestOut@xtest":    "notes_test.TestOut (test)",
		"go:internal/notes#file:notes_windows.go": "the file internal/notes/notes_windows.go (left out of the build)",
		"go:internal/notes#api:notes":             "what package notes exports",
		"go:internal/notes#var:_:notes.go:2":      "package notes's blank var in notes.go:2",
		"go:internal/notes#typedecl:env@test":     "notes.env as declared (test)",
	} {
		if got := Label(id, "notes"); got != want {
			t.Errorf("Label(%s) = %q, want %q", id, got, want)
		}
	}

	if got := Label("go:a/b#func:F", ""); got != "b.F" {
		t.Errorf("without a package name: %q", got)
	}

	long := Label(ID("go:x#func:"+strings.Repeat("A", 200)), "x")
	if n := len([]rune(long)); n != MaxLabel || !strings.Contains(long, "…") {
		t.Errorf("long label %q is %d long", long, n)
	}
}
