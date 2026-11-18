package graph

import (
	"go/parser"
	"go/token"
	"slices"
	"testing"
)

func sitesOf(t *testing.T, src string) (packageFacts, int) {
	t.Helper()

	f, err := parser.ParseFile(token.NewFileSet(), "x.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}

	pf := joinFiles([]fileFacts{scanFile(f)})

	return pf, len(pf.sites)
}

func TestShapesThatReadNoConcreteMethods(t *testing.T) {
	const head = "package x\n\nimport \"reflect\"\n\ntype Binder interface{ Bind(string) error }\n\n"

	cases := []struct {
		name  string
		body  string
		sites int
	}{
		{"a kind check guards the count", `func f(t reflect.Type) bool { return t.Kind() == reflect.Interface && t.NumMethod() == 0 }`, 0},
		{"a kind check of the value guards its type's count", `func f(v reflect.Value) bool { return v.Kind() == reflect.Interface && v.Type().NumMethod() == 0 }`, 0},
		{"a kind check of another value", `func f(v, w reflect.Value) bool { return w.Kind() == reflect.Interface && v.Type().NumMethod() == 0 }`, 1},
		{"a kind check joined by or", `func f(t reflect.Type) bool { return t.Kind() == reflect.Interface || t.NumMethod() == 0 }`, 1},
		{"the interface case of a switch on the kind", `func f(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Interface:
		return t.NumMethod() != 0
	}
	return false
}`, 0},
		{"a case with another kind beside it", `func f(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Interface, reflect.Struct:
		return t.NumMethod() != 0
	}
	return false
}`, 1},
		{"a count that only gates assertions", `func f(v reflect.Value) any {
	if v.Type().NumMethod() > 0 && v.CanInterface() {
		if u, ok := v.Interface().(Binder); ok {
			return u
		}
	}
	return nil
}`, 0},
		{"the gate with an else", `func f(v reflect.Value) any {
	if v.Type().NumMethod() > 0 && v.CanInterface() {
		if u, ok := v.Interface().(Binder); ok {
			return u
		}
	} else {
		return 1
	}
	return nil
}`, 1},
		{"the gate whose body uses the value otherwise", `func f(v reflect.Value) any {
	if v.Type().NumMethod() > 0 {
		return v.Interface()
	}
	return nil
}`, 1},
		{"the count used a second time", `func f(v reflect.Value) any {
	if n := v.Type().NumMethod(); n > 0 {
		if u, ok := v.Interface().(Binder); ok {
			return u
		}
	}
	return nil
}`, 1},
		{"a loosened gate", `func f(v reflect.Value) any {
	if v.Type().NumMethod() > 1 {
		if u, ok := v.Interface().(Binder); ok {
			return u
		}
	}
	return nil
}`, 1},
		{"an enumeration", `func f(t reflect.Type) []string {
	var out []string
	for i := range t.NumMethod() {
		out = append(out, t.Method(i).Name)
	}
	return out
}`, 2},
		{"a lookup by a literal name", `func f(v reflect.Value) reflect.Value { return v.MethodByName("Run") }`, 0},
		{"a lookup by a constant of the package", "const name = \"Run\"\n\nfunc f(v reflect.Value) reflect.Value { return v.MethodByName(name) }", 0},
		{"a lookup by a computed name", `func f(v reflect.Value, s string) reflect.Value { return v.MethodByName(s) }`, 1},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, got := sitesOf(t, head+c.body+"\n"); got != c.sites {
				t.Errorf("%d sites, want %d", got, c.sites)
			}
		})
	}
}

func TestTemplateText(t *testing.T) {
	const head = "package x\n\nimport \"text/template\"\n\n"

	pf, sites := sitesOf(t, head+"const page = \"{{.Total}} \" + \"{{$x.Owner.Name}}\"\n\nvar a = template.Must(template.New(\"a\").Parse(page))\n\nvar b = template.Must(template.New(\"b\").Parse(\"{{.Due}}\"))\n")
	if sites != 0 {
		t.Errorf("constant text: %d sites", sites)
	}

	for _, n := range []string{"Total", "Owner", "Name", "Due"} {
		if !slices.Contains(pf.literals, n) {
			t.Errorf("constant text: %s missing from %v", n, pf.literals)
		}
	}

	if _, sites := sitesOf(t, head+"func f(s string) *template.Template { return template.Must(template.New(\"a\").Parse(s)) }\n"); sites != 1 {
		t.Errorf("text from a parameter: %d sites, want 1", sites)
	}

	if _, sites := sitesOf(t, head+"func f() *template.Template { return template.Must(template.ParseGlob(\"*.html\")) }\n"); sites != 1 {
		t.Errorf("files at run time: %d sites, want 1", sites)
	}

	if _, sites := sitesOf(t, "package x\n\nimport \"net/url\"\n\nfunc f(s string) { _, _ = url.Parse(s) }\n"); sites != 0 {
		t.Errorf("a parse in a file without templates: %d sites, want 0", sites)
	}

	const both = "package x\n\nimport (\n\t\"html/template\"\n\t\"net/url\"\n)\n\n"

	if _, sites := sitesOf(t, both+"func f(s string) { _, _ = url.Parse(s); _ = template.New(\"a\") }\n"); sites != 0 {
		t.Errorf("another package's Parse beside templates: %d sites, want 0", sites)
	}

	if _, sites := sitesOf(t, both+"func f(s string) { url := template.New(\"a\"); _, _ = url.Parse(s); _ = url.URL{} }\n"); sites != 1 {
		t.Errorf("a template variable named like an import: %d sites, want 1", sites)
	}
}

func TestDotNames(t *testing.T) {
	got := dotNames("{{.Total}} {{ $x.owner.Name }} a.b.C1_x {{ .Ñame }} 3.5")
	if want := []string{"Total", "Name", "C1_x", "Ñame"}; !slices.Equal(got, want) {
		t.Errorf("dotNames = %v, want %v", got, want)
	}
}
