package decls

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen-plugins/sbom/go/goid"
)

const money = `package money

import (
	"fmt"
	"math/rand"
)

// Rate is the tax rate.
const Rate = 19

const (
	Cash = iota
	Card
)

// Amount is money in cents.
type Amount struct {
	Cents int ` + "`json:\"cents\"`" + `
}

func (a Amount) String() string { return fmt.Sprintf("%d", a.Cents) }

func Gross(a Amount) Amount {
	return Amount{Cents: a.Cents * (100 + Rate) / 100}
}

func Pick(n int) int { return rand.Intn(n) + len("x") }
`

func pkg(t *testing.T, files map[string]string) string {
	t.Helper()

	root := t.TempDir()

	for name, src := range files {
		p := filepath.Join(root, "money", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return root
}

func digests(t *testing.T, files map[string]string) (map[goid.ID]string, *Package) {
	t.Helper()

	p, err := Read(pkg(t, files), "money")
	if err != nil {
		t.Fatal(err)
	}

	out := map[goid.ID]string{}
	for _, u := range p.Units {
		out[u.ID] = u.Digest
	}

	return out, p
}

func changed(t *testing.T, base, head map[string]string) []string {
	t.Helper()

	b, _ := digests(t, base)
	h, _ := digests(t, head)

	var out []string

	for id, d := range h {
		if b[id] != d {
			out = append(out, string(id))
		}
	}

	for id := range b {
		if _, ok := h[id]; !ok {
			out = append(out, string(id)+" (gone)")
		}
	}

	slices.Sort(out)

	return out
}

func edit(src, old, repl string) string {
	if !strings.Contains(src, old) {
		panic("no " + old)
	}

	return strings.Replace(src, old, repl, 1)
}

func TestDigests(t *testing.T) {
	base := map[string]string{"money.go": money}

	cases := map[string]struct {
		head map[string]string
		want []string
	}{
		"a comment-only edit and a reflow change nothing": {
			head: map[string]string{"money.go": edit(edit(money, "// Rate is the tax rate.", "// Rate is the rate, in percent."),
				"return Amount{Cents: a.Cents * (100 + Rate) / 100}", "// rounded down\n\treturn Amount{\n\t\tCents: a.Cents * (100 + Rate) / 100,\n\t}")},
		},
		"a struct tag changes its type": {
			head: map[string]string{"money.go": edit(money, "json:\"cents\"", "json:\"c\"")},
			want: []string{"go:money#api:money", "go:money#type:Amount", "go:money#typedecl:Amount"},
		},
		"a method added in another file changes its type, not the method's siblings": {
			head: map[string]string{"money.go": money, "json.go": "package money\n\nfunc (a Amount) MarshalJSON() ([]byte, error) { return nil, nil }\n"},
			want: []string{"go:money#api:money", "go:money#method:Amount.MarshalJSON", "go:money#methodsig:Amount.MarshalJSON", "go:money#type:Amount"},
		},
		"a method's body changes the method only": {
			head: map[string]string{"money.go": edit(money, `fmt.Sprintf("%d", a.Cents)`, `fmt.Sprintf("%dc", a.Cents)`)},
			want: []string{"go:money#method:Amount.String"},
		},
		"a constant inserted into an iota group changes those after it": {
			head: map[string]string{"money.go": edit(money, "Cash = iota\n", "Cash = iota\n\tCheque\n")},
			want: []string{"go:money#api:money", "go:money#const:Card", "go:money#const:Cheque"},
		},
		"an import's path behind the same name changes its users": {
			head: map[string]string{"money.go": edit(money, `"math/rand"`, `"crypto/rand"`)},
			want: []string{"go:money#func:Pick"},
		},
		"a package-level name shadowing a predeclared one changes its users": {
			head: map[string]string{"money.go": money, "len.go": "package money\n\nfunc len(string) int { return 0 }\n"},
			want: []string{"go:money#api:money", "go:money#func:Pick", "go:money#func:len"},
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := changed(t, base, c.head); !slices.Equal(got, c.want) {
				t.Errorf("changed %v, want %v", got, c.want)
			}
		})
	}
}

func TestEmbed(t *testing.T) {
	src := "package money\n\nimport _ \"embed\"\n\n//go:embed rates.json\nvar rates []byte\n"

	base := map[string]string{"e.go": src, "rates.json": `{"eur": 19}`}
	head := map[string]string{"e.go": src, "rates.json": `{"eur": 20}`}

	if got := changed(t, base, head); !slices.Equal(got, []string{"go:money#var:rates"}) {
		t.Errorf("changed %v, want the embedding variable", got)
	}

	_, p := digests(t, base)
	if !slices.Contains(p.Inputs, "money/rates.json") || !slices.Contains(p.Inputs, "money/e.go") {
		t.Errorf("inputs %v lack the embedded file or the Go file", p.Inputs)
	}
}

func TestWhole(t *testing.T) {
	cases := map[string]map[string]string{
		"build constraints leave out": {"money.go": money, "other.go": "//go:build ignore\n\npackage money\n"},
		"cgo":                         {"money.go": money, "c.go": "package money\n\nimport \"C\"\n"},
		"go:linkname":                 {"money.go": money, "l.go": "package money\n\nimport _ \"unsafe\"\n\n//go:linkname now runtime.nanotime\nfunc now() int64\n"},
		"reflection by name":          {"money.go": money, "r.go": "package money\n\nimport \"reflect\"\n\nfunc f(v reflect.Value) { v.MethodByName(\"X\") }\n"},
		"non-Go files":                {"money.go": money, "a.s": "TEXT ·f(SB),0,$0\n"},
	}

	for want, files := range cases {
		t.Run(want, func(t *testing.T) {
			if _, p := digests(t, files); !strings.HasPrefix(p.Whole, want) {
				t.Errorf("Whole = %q, want %q", p.Whole, want)
			}
		})
	}

	if _, p := digests(t, map[string]string{"money.go": money}); p.Whole != "" {
		t.Errorf("a plain package is whole: %q", p.Whole)
	}
}

func TestUnits(t *testing.T) {
	_, p := digests(t, map[string]string{"money.go": money, "money_test.go": "package money\n\nfunc helper() {}\n"})

	var gross, rate, helper *Unit

	for i, u := range p.Units {
		switch u.ID {
		case "go:money#func:Gross":
			gross = &p.Units[i]
		case "go:money#const:Rate":
			rate = &p.Units[i]
		case "go:money#func:helper@test":
			helper = &p.Units[i]
		}
	}

	if gross == nil || !gross.Executable || gross.File != "money/money.go" || gross.Line == 0 || gross.EndLine <= gross.Line {
		t.Errorf("Gross: %+v", gross)
	}

	if rate == nil || rate.Executable {
		t.Errorf("Rate: %+v", rate)
	}

	if helper == nil || !helper.Empty {
		t.Errorf("test helper: %+v", helper)
	}
}

func TestGuessName(t *testing.T) {
	for path, want := range map[string]string{
		"math/rand":                  "rand",
		"github.com/jackc/pgx/v5":    "pgx",
		"gopkg.in/yaml.v3":           "yaml",
		"github.com/x/go-cmp":        "cmp",
		"github.com/x/client-go":     "client",
		"github.com/x/weird-name.io": "",
	} {
		got, ok := guessName(path)
		if !ok {
			got = ""
		}

		if got != want {
			t.Errorf("%s: %q, want %q", path, got, want)
		}
	}
}

func TestDigestFrom(t *testing.T) {
	src := "package money\n\nvar swaggerSpec = []string{\"H4sI\"}\n\nvar rawSpec = decode(swaggerSpec)\n\nfunc decode(s []string) []byte { return nil }\n"
	cfg := Config{DigestFrom: []DigestFrom{{
		Units: []string{"go:*#var:swaggerSpec", "go:*#var:rawSpec"},
		From:  []string{"api:money/api.yaml#/servers", "api:money/api.yaml#/openapi"},
	}}}

	p, err := ReadWith(pkg(t, map[string]string{"spec.go": src}), "money", cfg)
	if err != nil {
		t.Fatal(err)
	}

	for _, u := range p.Units {
		taken := u.ID == "go:money#var:swaggerSpec" || u.ID == "go:money#var:rawSpec"
		if taken != (u.Digest == "" && slices.Equal(u.DigestFrom, cfg.DigestFrom[0].From)) {
			t.Errorf("%s: digest %q from %v", u.ID, u.Digest, u.DigestFrom)
		}
	}

	for pat, id := range map[string]string{"go:*#var:x": "go:a/b#var:x", "go:a/*": "go:a/b#func:F", "*#var:*": "go:a#var:y"} {
		if !matchID(pat, id) {
			t.Errorf("%s doesn't match %s", pat, id)
		}
	}

	if matchID("go:*#var:x", "go:a#var:xy") {
		t.Error("a pattern matched past its end")
	}
}

func TestBuildContextIsFixed(t *testing.T) {
	files := map[string]string{
		"money.go":    money,
		"cover.go":    "//go:build pipelinegen_cover\n\npackage money\n\nfunc covered() {}\n",
		"win.go":      "//go:build windows\n\npackage money\n\nfunc win() {}\n",
		"int_test.go": "//go:build integration\n\npackage money\n\nfunc slow() {}\n",
	}

	ids, p := digests(t, files)

	if _, ok := ids["go:money#func:covered"]; !ok {
		t.Error("the coverage build's file is left out")
	}

	for _, id := range []goid.ID{"go:money#file:win.go", "go:money#file:int_test.go"} {
		if ids[id] == "" {
			t.Errorf("%s: no unit for a left-out file", id)
		}
	}

	if _, ok := ids["go:money#func:win"]; ok {
		t.Error("a left-out file's declarations are units")
	}

	if p.Whole != "build constraints leave out win.go" {
		t.Errorf("Whole = %q", p.Whole)
	}

	head := maps.Clone(files)

	head["win.go"] = edit(files["win.go"], "func win() {}", "func win() { _ = 1 }")
	if got := changed(t, files, head); !slices.Equal(got, []string{"go:money#file:win.go"}) {
		t.Errorf("changed %v, want the left-out file's unit", got)
	}

	p2, err := ReadWith(pkg(t, files), "money", Config{Build: Build{GOOS: "windows", Tags: []string{}}})
	if err != nil {
		t.Fatal(err)
	}

	if !slices.ContainsFunc(p2.Units, func(u Unit) bool { return u.ID == "go:money#func:win" }) ||
		!slices.ContainsFunc(p2.Units, func(u Unit) bool { return u.ID == "go:money#file:cover.go" }) {
		t.Error("the configured build isn't the one read")
	}
}

func TestTestTypeDecl(t *testing.T) {
	src := "package money\n\ntype env struct{ n int }\n\nfunc (e *env) a() int { return e.n }\n"
	base := map[string]string{"money.go": money, "env_test.go": src}
	head := map[string]string{"money.go": money, "env_test.go": src + "\nfunc (e *env) b() int { return 2 }\n"}

	want := []string{"go:money#method:env.b@test", "go:money#type:env@test"}
	if got := changed(t, base, head); !slices.Equal(got, want) {
		t.Errorf("an added test method changes %v, want %v and not the type's own part", got, want)
	}

	ids, _ := digests(t, base)
	if ids["go:money#typedecl:env@test"] == "" {
		t.Error("no typedecl unit for a test type with methods")
	}

	if _, ok := ids["go:money#methodsig:env.a@test"]; ok {
		t.Error("a test type has a methodsig unit")
	}

	fields := map[string]string{"money.go": money, "env_test.go": strings.Replace(src, "n int", "n, m int", 1)}
	if got := changed(t, base, fields); !slices.Contains(got, "go:money#typedecl:env@test") {
		t.Errorf("a new field changes %v, want the typedecl unit among them", got)
	}
}

func TestUnparsedPackage(t *testing.T) {
	root := pkg(t, map[string]string{
		"money.go":      money,
		"broken.go":     "package money\n\nfunc Broken( {\n",
		"money_test.go": "package money\n\nfunc helper() {}\n",
		"other.go":      "//go:build ignore\n\npackage money\n",
	})

	p, err := Read(root, "money")
	if err != nil {
		t.Fatal(err)
	}

	var ids []string
	for _, u := range p.Units {
		ids = append(ids, string(u.ID))
	}

	want := []string{"go:money#file:broken.go", "go:money#file:money.go", "go:money#file:money_test.go", "go:money#file:other.go"}
	if !slices.Equal(ids, want) {
		t.Errorf("units %q, want %q", ids, want)
	}

	if !strings.HasPrefix(p.Unparsed, "broken.go:3:") || !strings.HasPrefix(p.Whole, "a file doesn't parse: broken.go:3:") {
		t.Errorf("Unparsed %q, Whole %q", p.Unparsed, p.Whole)
	}

	if !slices.Equal(p.Inputs, []string{"money/broken.go", "money/money.go", "money/money_test.go", "money/other.go"}) {
		t.Errorf("inputs %q", p.Inputs)
	}

	before := p.Units[1].Digest

	if err := os.WriteFile(filepath.Join(root, "money", "money.go"), []byte(money+"\nvar x = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if q, err := Read(root, "money"); err != nil || q.Units[1].Digest == before {
		t.Errorf("an edit to a file of the package leaves its unit: %v", err)
	}
}

func TestTypeDeclAndMethodSigs(t *testing.T) {
	generic := "package money\n\ntype List[T any] struct{ items []T }\n\nfunc (l *List[T]) Push(v T) { l.items = append(l.items, v) }\n"
	alias := "package money\n\ntype Cash = Amount\n\nfunc (c Cash) Twice() Amount { return Amount{Cents: 2 * c.Cents} }\n"
	iface := "package money\n\ntype Pricer interface{ Price() Amount }\n"
	inTest := "package money\n\nfunc (a Amount) debug() string { return \"\" }\n"

	base := map[string]string{"money.go": money, "list.go": generic, "cash.go": alias, "pricer.go": iface, "debug_test.go": inTest}

	ids, _ := digests(t, base)
	for _, id := range []string{
		"go:money#typedecl:Amount", "go:money#methodsig:Amount.String",
		"go:money#typedecl:List", "go:money#methodsig:List.Push",
		// keyed by the receiver as written: the alias
		"go:money#typedecl:Cash", "go:money#methodsig:Cash.Twice",
		"go:money#methodsig:Amount.debug@test",
	} {
		if ids[goid.ID(id)] == "" {
			t.Errorf("no unit %s", id)
		}
	}

	for _, id := range []string{"go:money#typedecl:Pricer", "go:money#typedecl:Rate"} {
		if _, ok := ids[goid.ID(id)]; ok {
			t.Errorf("unit %s: an interface or a type without methods has none", id)
		}
	}

	cases := map[string]struct {
		head map[string]string
		want []string
	}{
		"a method added": {
			head: map[string]string{"extra.go": "package money\n\nfunc (a Amount) Half() Amount { return Amount{Cents: a.Cents / 2} }\n"},
			want: []string{"go:money#api:money", "go:money#method:Amount.Half", "go:money#methodsig:Amount.Half", "go:money#type:Amount"},
		},
		"a value receiver turned into a pointer": {
			head: map[string]string{"money.go": edit(money, "func (a Amount) String()", "func (a *Amount) String()")},
			want: []string{"go:money#api:money", "go:money#method:Amount.String", "go:money#methodsig:Amount.String", "go:money#type:Amount"},
		},
		"a field added": {
			head: map[string]string{"money.go": edit(money, "Cents int", "Cents, Tax int")},
			want: []string{"go:money#api:money", "go:money#type:Amount", "go:money#typedecl:Amount"},
		},
		"a method moved to another file": {
			head: map[string]string{
				"money.go":  edit(money, "func (a Amount) String() string { return fmt.Sprintf(\"%d\", a.Cents) }\n", ""),
				"string.go": "package money\n\nimport \"fmt\"\n\nfunc (a Amount) String() string { return fmt.Sprintf(\"%d\", a.Cents) }\n",
			},
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			head := maps.Clone(base)
			maps.Copy(head, c.head)

			if got := changed(t, base, head); !slices.Equal(got, c.want) {
				t.Errorf("changed %v, want %v", got, c.want)
			}
		})
	}
}

func TestUnexportedMethodsOutOfTheDigest(t *testing.T) {
	helper := "package money\n\nfunc (a Amount) round() int { return a.Cents }\n"
	base := map[string]string{"money.go": money}

	for name, head := range map[string]map[string]string{
		"added":   {"money.go": money, "round.go": helper},
		"renamed": {"money.go": money, "round.go": strings.Replace(helper, "round()", "rounded()", 1)},
	} {
		got := changed(t, base, head)
		if slices.Contains(got, "go:money#type:Amount") || slices.Contains(got, "go:money#typedecl:Amount") {
			t.Errorf("%s: changed %v, want the type as it was", name, got)
		}
	}

	if got := changed(t, map[string]string{"money.go": money, "round.go": helper}, base); slices.Contains(got, "go:money#type:Amount") {
		t.Errorf("removed: changed %v, want the type as it was", got)
	}

	_, p := digests(t, map[string]string{"money.go": money, "round.go": helper})
	if want := []goid.ID{"go:money#methodsig:Amount.round"}; !slices.Equal(p.Private["go:money#type:Amount"], want) {
		t.Errorf("private %v, want %v", p.Private, want)
	}

	for name, other := range map[string]string{
		"an interface declares it": "package money\n\ntype rounder interface{ round() int }\n",
		"a test file's interface":  "package money\n\nfunc check(x any) bool { _, ok := x.(interface{ round() int }); return ok }\n",
		"a field bears it":         "package money\n\ntype box struct{ round int }\n",
		"another type's method":    "package money\n\ntype other struct{}\n\nfunc (other) round() int { return 0 }\n",
	} {
		file := "other.go"
		if name == "a test file's interface" {
			file = "other_test.go"
		}

		b := map[string]string{"money.go": money, file: other}
		h := map[string]string{"money.go": money, file: other, "round.go": helper}

		if got := changed(t, b, h); !slices.Contains(got, "go:money#type:Amount") {
			t.Errorf("%s: changed %v, want the type", name, got)
		}
	}
}

func TestShadowingThroughEmbedding(t *testing.T) {
	src := "package money\n\ntype inner struct{}\n\nfunc (inner) pay() int { return 1 }\n\ntype mid struct{ inner }\n\nfunc (mid) keep() {}\n\ntype outer struct{ mid }\n\nfunc use(o outer) int { return o.pay() }\n"
	base := map[string]string{"money.go": money, "pay.go": src}
	head := map[string]string{"money.go": money, "pay.go": src + "\nfunc (mid) pay() int { return 2 }\n"}

	if got := changed(t, base, head); !slices.Contains(got, "go:money#type:mid") {
		t.Errorf("changed %v, want mid's type: outer.pay resolves to mid's method now", got)
	}
}
