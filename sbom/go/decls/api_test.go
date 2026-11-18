package decls

import (
	"maps"
	"testing"

	"github.com/sideways-works/pipelinegen-plugins/sbom/go/goid"
)

const shop = `package money

import (
	str "strconv"
	up "strings"
)

type Amount struct {
	Cents int
	note  string
}

type List[T any, K comparable] struct{ items []T }

func (l List[T, K]) Len() int { return len(l.items) }

func Map[T any, U comparable](in []T, f func(T) U) []U { return nil }

func Name(s string) string {
	return up.ToUpper(s)
}

func Noop() {}

func Format(n int) string { return str.Itoa(n) }

func Parse(s string) (int, *str.NumError) { return 0, nil }

var Default Amount = Amount{Cents: 100}

var Zero = Amount{}

var _ = Noop

func init() { Default.Cents = 1 }
`

const shopTest = `package money

import "testing"

func TestName(t *testing.T) { _ = Name("x") }
`

func apiDigest(t *testing.T, files map[string]string) string {
	t.Helper()

	d, _ := digests(t, files)

	id := goid.New("money", goid.API, "money")
	if d[id] == "" {
		t.Fatalf("no %s among %v", id, d)
	}

	return d[id]
}

func TestAPIDigest(t *testing.T) {
	base := map[string]string{"money.go": shop, "money_test.go": shopTest}
	b := apiDigest(t, base)

	for name, c := range map[string]struct {
		head map[string]string
		same bool
	}{
		"a body edit that keeps statements":                {map[string]string{"money.go": edit(shop, "up.ToUpper(s)", "up.ToLower(s)")}, true},
		"a typed variable's initialiser":                   {map[string]string{"money.go": edit(shop, "Amount{Cents: 100}", "Amount{Cents: 200}")}, true},
		"an init's body":                                   {map[string]string{"money.go": edit(shop, "Default.Cents = 1", "Default.Cents = 2")}, true},
		"a blank variable":                                 {map[string]string{"money.go": edit(shop, "var _ = Noop", "var _ = Name")}, true},
		"a test file":                                      {map[string]string{"money_test.go": edit(shopTest, `Name("x")`, `Name("y")`)}, true},
		"an import alias in bodies renamed, the same path": {map[string]string{"money.go": edit(edit(shop, `up "strings"`, `upper "strings"`), "up.ToUpper", "upper.ToUpper")}, true},
		"a declaration moved to another file":              {map[string]string{"money.go": edit(shop, "func Noop() {}\n", ""), "noop.go": "package money\n\nfunc Noop() {}\n"}, true},
		"an empty body gets statements":                    {map[string]string{"money.go": edit(shop, "func Noop() {}", "func Noop() { _ = 1 }")}, false},
		"a body goes":                                      {map[string]string{"money.go": edit(shop, "func Noop() {}", "func Noop()")}, false},
		"an unexported field":                              {map[string]string{"money.go": edit(shop, "note  string", "note  int")}, false},
		"an inferred variable's initialiser":               {map[string]string{"money.go": edit(shop, "Zero = Amount{}", "Zero = &Amount{}")}, false},
		"an import path behind a signature's name":         {map[string]string{"money.go": edit(shop, `str "strconv"`, `str "example.com/strconv"`)}, false},
		"a method added to a type in another file":         {map[string]string{"money.go": shop, "json.go": "package money\n\nfunc (a Amount) MarshalJSON() ([]byte, error) { return nil, nil }\n"}, false},
		"a type parameter's constraint":                    {map[string]string{"money.go": edit(shop, "List[T any, K comparable]", "List[T comparable, K comparable]")}, false},
		"a generic function's type parameters":             {map[string]string{"money.go": edit(shop, "Map[T any, U comparable]", "Map[T any, U any]")}, false},
		"a signature":                                      {map[string]string{"money.go": edit(shop, "func Format(n int) string", "func Format(n int64) string")}, false},
	} {
		head := map[string]string{"money.go": shop, "money_test.go": shopTest}
		maps.Copy(head, c.head)

		if got := apiDigest(t, head) == b; got != c.same {
			t.Errorf("%s: API unchanged %v, want %v", name, got, c.same)
		}
	}
}

func TestAPIOnlyWithCode(t *testing.T) {
	d, _ := digests(t, map[string]string{"money_test.go": shopTest})

	for id := range d {
		if id.Kind() == goid.API {
			t.Errorf("a test-only package lists %s", id)
		}
	}
}
