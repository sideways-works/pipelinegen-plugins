package graph

import (
	"slices"
	"testing"
)

// compileOutside is a dependency that parses the template text its caller
// hands in and returns the template, which the caller executes itself.
var compileOutside = map[string]string{
	"press/vendor/modules.txt": "# example.com/tpl v1.0.0\n## explicit; go 1.24\nexample.com/tpl\nexample.com/tpl/compile\nexample.com/tpl/wrap\n",
	"press/vendor/example.com/tpl/compile/compile.go": `package compile

import "text/template"

func Compile(text string) *template.Template { return template.Must(template.New("c").Parse(text)) }
`,
}

// executes are the tree's packages that execute templates: each of them
// hands docs.A to a template whose text the scan read, and docs.C to one
// it did not.
var executes = map[string]string{
	// the dependency's template, executed where it is returned and from a
	// variable
	"press/exec/exec.go": `package exec

import (
	"io"
	"os"

	"example.com/press/docs"
	"example.com/tpl/compile"
)

func Show(w io.Writer) error {
	b, _ := os.ReadFile("page.txt")

	return compile.Compile(string(b)).Execute(w, docs.C{})
}

func Kept(w io.Writer, text string) error {
	t := compile.Compile(text)

	return t.ExecuteTemplate(w, "c", docs.C{})
}
`,
	// a package variable parsed from a constant, in a package that also
	// parses a URL it is handed: url.Parse parses no template
	"press/known/known.go": `package known

import (
	"io"
	"net/url"
	"text/template"

	"example.com/press/docs"
)

func Link(s string) string {
	u, _ := url.Parse(s)

	return u.Path
}

const pageText = "{{.Body}}"

var page = template.Must(template.New("page").Parse(pageText))

func Show(w io.Writer) error { return page.Execute(w, docs.A{}) }
`,
	// a file the build leaves out, which writes the variable in another
	// build only
	"press/known/known_other.go": `//go:build other

package known

import (
	"os"

	"example.com/tpl/compile"
)

func init() { page = compile.Compile(os.Getenv("PAGE")) }
`,
	// a field, written from a parse the scan read and from the dependency
	"press/field/field.go": `package field

import (
	"io"
	"text/template"

	"example.com/press/docs"
	"example.com/tpl/compile"
)

type Pages struct{ t *template.Template }

func New() *Pages { return &Pages{t: template.Must(template.New("p").Parse("{{.Body}}"))} }

func (p *Pages) Show(w io.Writer) error { return p.t.Execute(w, docs.A{}) }

type Loaded struct{ t *template.Template }

func Load(text string) *Loaded {
	l := &Loaded{}
	l.t = compile.Compile(text)

	return l
}

func (l *Loaded) Show(w io.Writer) error { return l.t.Execute(w, docs.C{}) }
`,
	// a template a function of the tree returns
	"press/fn/fn.go": `package fn

import (
	"io"
	"text/template"

	"example.com/press/docs"
	"example.com/tpl/compile"
)

func page() *template.Template {
	t := template.New("p")

	return template.Must(t.Parse("{{.Body}}"))
}

func Show(w io.Writer) error { return page().Execute(w, docs.A{}) }

func loaded(text string) *template.Template { return compile.Compile(text) }

func Load(w io.Writer, text string) error { return loaded(text).Execute(w, docs.C{}) }
`,
}

// executesHidden execute a template through an interface with its
// Execute, and through Execute taken as a value.
var executesHidden = map[string]string{
	"press/iface/iface.go": `package iface

import (
	"io"

	"example.com/press/docs"
)

type runner interface {
	Execute(w io.Writer, data any) error
}

func Run(r runner, w io.Writer) error { return r.Execute(w, docs.C{}) }
`,
	"press/later/later.go": `package later

import (
	"io"

	"example.com/press/docs"
	"example.com/tpl/compile"
)

func Later(w io.Writer, text string) error {
	f := compile.Compile(text).Execute

	return f(w, docs.C{})
}
`,
}

func TestATemplateOfADependencyBindsWhatItIsHandedWhole(t *testing.T) {
	root := pressRoot(t, compileOutside, executes)

	for _, dir := range []string{"press/exec", "press/field", "press/fn"} {
		if got := wholeFor(t, root, dir); !slices.Contains(got, "go:press/docs#type:C") {
			t.Errorf("%s: whole method sets %v, want the document executed on the dependency's template", dir, got)
		}
	}

	for _, dir := range []string{"press/known", "press/field", "press/fn"} {
		if got := wholeFor(t, root, dir); slices.Contains(got, "go:press/docs#type:A") {
			t.Errorf("%s: whole method sets %v, want no document executed on a template the scan read", dir, got)
		}
	}

	if got := wholeFor(t, root, "press/hold"); len(got) != 0 {
		t.Errorf("a package that executes no template binds %v whole", got)
	}
}

func TestATemplateExecutedOutOfSightBindsItsDataWhole(t *testing.T) {
	root := pressRoot(t, compileOutside, executesHidden)

	for _, dir := range []string{"press/iface", "press/later"} {
		if got := wholeFor(t, root, dir); !slices.Contains(got, "go:press/docs#type:C") {
			t.Errorf("%s: whole method sets %v, want the document handed to Execute", dir, got)
		}
	}
}
