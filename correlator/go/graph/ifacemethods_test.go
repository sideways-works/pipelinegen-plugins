package graph

import (
	"slices"
	"strings"
	"testing"
)

// kit is a module whose server type has handlers as methods, used by code
// that never hands it to an interface, beside interfaces, a generic list
// and a chain of embedded types.
var kit = map[string]string{
	"kit/go.mod": "module example.com/kit\n\ngo 1.24\n",
	"kit/srv/srv.go": `package srv

type Server struct{ n int }

func (s *Server) Refund() int { return s.n }

func (s *Server) Guard() bool { return s.n > 0 }

func New() *Server { return &Server{n: 1} }
`,
	"kit/use/use.go": `package use

import (
	"fmt"

	"example.com/kit/srv"
)

func Count(s *srv.Server) int { return s.Refund() }

func Hold() *srv.Server { return srv.New() }

func Show(x any) string { return fmt.Sprint(x) }
`,
	"kit/gate/gate.go": `package gate

type Guarded interface{ Guard() bool }

func Check(x any) bool {
	g, ok := x.(Guarded)

	return ok && g.Guard()
}
`,
	"kit/deep/deep.go": `package deep

type G struct{}

func (G) M() string { return "g" }

type E struct{ G }

type U struct{ E }

func Call(u U) string { return u.M() }
`,
	"kit/list/list.go": `package list

type List[T any] struct{ items []T }

func (l *List[T]) Push(x T) { l.items = append(l.items, x) }

type Sizer interface{ Size() int }

func Total[T Sizer](xs []T) int {
	n := 0
	for _, x := range xs {
		n += x.Size()
	}

	return n
}
`,
	"kit/list/use.go": `package list

func Fill() int {
	var l List[int]
	l.Push(1)

	return len(l.items)
}
`,
}

func kitRoot(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	for rel, src := range kit {
		write(t, root, rel, src)
	}

	return root
}

func kitSlices() []Slice {
	slice := func(name string, list ...string) Slice {
		return Slice{Suite: "backend", Name: name, HasList: true, List: list}
	}

	return []Slice{
		slice("refund", "go:kit/use#func:Count", "go:kit/srv#method:Server.Refund"),
		slice("hold", "go:kit/use#func:Hold", "go:kit/srv#func:New"),
		slice("check", "go:kit/gate#func:Check", "go:kit/use#func:Hold", "go:kit/srv#func:New"),
		slice("deep", "go:kit/deep#func:Call", "go:kit/deep#method:G.M"),
		slice("fill", "go:kit/list#func:Fill", "go:kit/list#method:List.Push"),
	}
}

func TestInterfaceMethods(t *testing.T) {
	all := []string{"check", "deep", "fill", "hold", "refund"}

	cases := map[string]struct {
		edit func(t *testing.T, root string)
		// narrow is what the change selects with the narrowing on, wide
		// with it off
		narrow, wide []string
	}{
		"a server gains a method no interface names: only what changed runs": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				write(t, root, "kit/srv/start.go", "package srv\n\nfunc (s *Server) StartRefusalRefund() int { return s.n * 2 }\n")
			},
			narrow: nil, wide: []string{"check", "hold", "refund"},
		},
		"a handler that ran changes: the slice that ran it": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "kit/srv/srv.go", "return s.n }", "return s.n + 1 }")
			},
			narrow: []string{"refund"}, wide: []string{"refund"},
		},
		"a server gains String, which fmt.Stringer names": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				write(t, root, "kit/srv/str.go", "package srv\n\nfunc (s *Server) String() string { return \"srv\" }\n")
			},
			narrow: []string{"check", "hold", "refund"}, wide: []string{"check", "hold", "refund"},
		},
		"a server gains Error, which error names": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				write(t, root, "kit/srv/err.go", "package srv\n\nfunc (s *Server) Error() string { return \"srv\" }\n")
			},
			narrow: []string{"check", "hold", "refund"}, wide: []string{"check", "hold", "refund"},
		},
		"a server gains Unwrap, which only an interface literal in errors names": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				write(t, root, "kit/srv/unwrap.go", "package srv\n\nfunc (s *Server) Unwrap() error { return nil }\n")
			},
			narrow: []string{"check", "hold", "refund"}, wide: []string{"check", "hold", "refund"},
		},
		"an interface gains a method": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "kit/gate/gate.go", "Guard() bool }", "Guard() bool; Close() }")
			},
			narrow: []string{"check"}, wide: []string{"check"},
		},
		"a method an interface names goes from a value to a pointer receiver": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "kit/srv/srv.go", "func (s *Server) Guard()", "func (s Server) Guard()")
			},
			narrow: []string{"check", "hold", "refund"}, wide: []string{"check", "hold", "refund"},
		},
		"a method only a call names goes from a pointer to a value receiver: its callers": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "kit/srv/srv.go", "func (s *Server) Refund()", "func (s Server) Refund()")
			},
			narrow: []string{"refund"}, wide: []string{"check", "hold", "refund"},
		},
		"a method an interface names is removed": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "kit/srv/srv.go", "func (s *Server) Guard() bool { return s.n > 0 }\n", "")
			},
			narrow: []string{"check", "hold", "refund"}, wide: []string{"check", "hold", "refund"},
		},
		"a method that shadows a promoted one: the caller whose text didn't change": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				write(t, root, "kit/deep/e.go", "package deep\n\nfunc (E) M() string { return \"e\" }\n")
			},
			narrow: []string{"deep"}, wide: []string{"deep"},
		},
		"a generic type gains a method an interface names": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				write(t, root, "kit/list/len.go", "package list\n\nfunc (l *List[T]) Size() int { return len(l.items) }\n")
			},
			narrow: []string{"fill"}, wide: []string{"fill"},
		},
		"a generic type gains a method nothing names": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				write(t, root, "kit/list/peek.go", "package list\n\nfunc (l *List[T]) Rotate() T { return l.items[0] }\n")
			},
			narrow: nil, wide: []string{"fill"},
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			base, head := kitRoot(t), kitRoot(t)
			c.edit(t, head)

			on := atLevel2()
			if got := bothWays(t, base, head, on, kitSlices()); !slices.Equal(got, c.narrow) {
				t.Errorf("on: selects %v, want %v", got, c.narrow)
			}

			delete(on, NarrowInterfaceMethods)

			if got := bothWays(t, base, head, on, kitSlices()); !slices.Equal(got, c.wide) {
				t.Errorf("off: selects %v, want %v (of %v)", got, c.wide, all)
			}
		})
	}
}

func TestNamesLinksLiveInTheirOwnSet(t *testing.T) {
	refund := Link{From: "go:kit/srv#typedecl:Server", To: "go:kit/srv#methodsig:Server.Refund", Kind: KindBind}

	cases := map[string]func(t *testing.T, root string){
		"an interface in a package that doesn't import the type's": func(t *testing.T, root string) {
			t.Helper()
			write(t, root, "kit/other/other.go", "package other\n\ntype Refunder interface{ Refund() int }\n")
		},
		"an interface literal in a function body": func(t *testing.T, root string) {
			t.Helper()
			write(t, root, "kit/other/other.go", "package other\n\nfunc Is(x any) bool { _, ok := x.(interface{ Refund() int }); return ok }\n")
		},
		"an interface in a test file": func(t *testing.T, root string) {
			t.Helper()
			write(t, root, "kit/gate/gate_test.go", "package gate\n\ntype refunder interface{ Refund() int }\n")
		},
		"a module version whose function body asserts an interface literal": func(t *testing.T, root string) {
			t.Helper()
			vendored(t, root, "v1.1.0", "func Is(x any) bool { _, ok := x.(interface{ Refund() int }); return ok }")
		},
	}

	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			base, head := kitRoot(t), kitRoot(t)
			vendored(t, base, "v1.0.0", "func Is(x any) bool { return x != nil }")

			if !strings.HasPrefix(name, "a module version") {
				vendored(t, head, "v1.0.0", "func Is(x any) bool { return x != nil }")
			}

			change(t, head)

			var sets [2][]LinkSet

			for i, root := range []string{base, head} {
				sets[i] = allSets(t, load(t, root))

				for _, s := range sets[i][:len(sets[i])-1] {
					for _, l := range s.Links {
						if strings.Contains(l.To, "#methodsig:") && strings.Contains(l.From, "#typedecl:") || l.From == AllMethodsNode {
							t.Errorf("%s: the package set holds %v, which depends on N", s.Group, l)
						}
					}
				}
			}

			baseNames, headNames := sets[0][len(sets[0])-1], sets[1][len(sets[1])-1]
			if slices.Contains(baseNames.Links, refund) || !slices.Contains(headNames.Links, refund) {
				t.Errorf("names set: %v in the base %t, in the head %t; want only in the head", refund,
					slices.Contains(baseNames.Links, refund), slices.Contains(headNames.Links, refund))
			}

			if slices.Equal(baseNames.Inputs, headNames.Inputs) && !strings.HasPrefix(name, "a module version") {
				t.Errorf("the names set's inputs didn't change: %v", headNames.Inputs)
			}

			if !slices.Contains(headNames.Inputs, "kit/go.mod") {
				t.Errorf("the names set doesn't read kit/go.mod: %v", headNames.Inputs)
			}

			// the type's own package set is the same in both trees: what
			// changed is keyed by the names set alone
			srv := func(ss []LinkSet) []Link {
				i := slices.IndexFunc(ss, func(s LinkSet) bool { return s.Group == "kit/srv" })

				return ss[i].Links
			}

			if a, b := srv(sets[0]), srv(sets[1]); !slices.Equal(a, b) {
				t.Errorf("kit/srv's set changed: %v", linkDiff(a, b))
			}
		})
	}
}

func vendored(t *testing.T, root, version, body string) {
	t.Helper()

	write(t, root, "kit/go.mod", "module example.com/kit\n\ngo 1.24\n\nrequire example.com/ext "+version+"\n")
	write(t, root, "kit/vendor/modules.txt", "# example.com/ext "+version+"\n## explicit; go 1.24\nexample.com/ext\n")
	write(t, root, "kit/vendor/example.com/ext/ext.go", "package ext\n\n"+body+"\n")
	write(t, root, "kit/ext/ext.go", "package ext\n\nimport outside \"example.com/ext\"\n\nvar Is = outside.Is\n")
}
