package graph

import (
	"slices"
	"testing"
)

func whRoot(t *testing.T) string {
	t.Helper()

	root := t.TempDir()

	for rel, src := range map[string]string{
		"wh/go.mod":              "module example.com/wh\n\ngo 1.24\n",
		"wh/w/w.go":              "package w\n\nfunc Do() int { return 1 }\n",
		"wh/w/x.s":               "// nothing to assemble\n",
		"wh/w/w_test.go":         "package w\n\nimport \"testing\"\n\nfunc TestDo(t *testing.T) {\n\tif Do()+helper() != 2 {\n\t\tt.Fatal()\n\t}\n}\n",
		"wh/w/helper_test.go":    "package w\n\nfunc helper() int { return 1 }\n",
		"wh/user/user.go":        "package user\n\nimport \"example.com/wh/w\"\n\nfunc F() int { return w.Do() }\n",
		"wh/user/user_test.go":   "package user\n\nimport \"testing\"\n\nfunc TestF(t *testing.T) { _ = F() }\n",
		"wh/other/other.go":      "package other\n\nfunc G() int { return 2 }\n",
		"wh/other/other_test.go": "package other\n\nimport \"testing\"\n\nfunc TestG(t *testing.T) { _ = G() }\n",
	} {
		write(t, root, rel, src)
	}

	return root
}

func whSlices() []Slice {
	return []Slice{
		{Suite: "backend", Name: "user", HasList: true, Sources: []string{"wh/user/user_test.go"}, List: []string{"go:wh/user#func:F", "go:wh/w#func:Do"}},
		{Suite: "backend", Name: "wtest", HasList: true, Sources: []string{"wh/w/w_test.go"}, List: []string{"go:wh/w#func:Do"}},
		{Suite: "backend", Name: "other", HasList: true, Sources: []string{"wh/other/other_test.go"}, List: []string{"go:wh/other#func:G"}},
	}
}

func TestAWholePackagesTestsAreBoundAsUsual(t *testing.T) {
	cases := map[string]struct {
		edit func(t *testing.T, root string)
		want []string
	}{
		"a test added": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				write(t, root, "wh/w/new_test.go", "package w\n\nimport \"testing\"\n\nfunc TestNew(t *testing.T) {}\n")
			},
		},
		"a helper's body": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "wh/w/helper_test.go", "return 1", "return 0 + 1")
			},
			want: []string{"wtest"},
		},
		"its own code nothing recorded running": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				write(t, root, "wh/w/more.go", "package w\n\nfunc More() int { return 3 }\n")
			},
			want: []string{"user", "wtest"},
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			base, head := whRoot(t), whRoot(t)
			c.edit(t, head)

			if got := bothWays(t, base, head, atLevel2(), whSlices()); !slices.Equal(got, c.want) {
				t.Errorf("selects %v, want %v", got, c.want)
			}
		})
	}
}

func TestATestFilesLookupByNameIsWholeOnlyWithTheNarrowingOff(t *testing.T) {
	lookups := map[string]string{
		"a computed name": `type api struct{}

func (api) Ping() int { return Do() }

func TestPing(t *testing.T) {
	for _, n := range []string{"Pi" + "ng"} {
		_ = reflect.ValueOf(api{}).MethodByName(n)
	}
}
`,
		"an interface's method names": `type API interface{ Ping() int }

type app struct{}

func (app) Ping() int { return Do() }

func TestPing(t *testing.T) {
	api := reflect.TypeFor[API]()
	srv := reflect.ValueOf(app{})

	for i := range api.NumMethod() {
		_ = srv.MethodByName(api.Method(i).Name)
	}
}
`,
	}

	for name, lookup := range lookups {
		t.Run(name, func(t *testing.T) {
			root := func() string {
				r := t.TempDir()
				write(t, r, "rf/go.mod", "module example.com/rf\n\ngo 1.24\n")
				write(t, r, "rf/r/r.go", "package r\n\nfunc Do() int { return 1 }\n")
				write(t, r, "rf/r/r_test.go", "package r\n\nimport (\n\t\"reflect\"\n\t\"testing\"\n)\n\n"+lookup)

				return r
			}

			ss := []Slice{{Suite: "backend", Name: "rtest", HasList: true, Sources: []string{"rf/r/r_test.go"}, List: []string{"go:rf/r#func:Do"}}}

			base, head := root(), root()
			write(t, head, "rf/r/new_test.go", "package r\n\nimport \"testing\"\n\nfunc TestNew(t *testing.T) {}\n")

			off := atLevel2()
			delete(off, NarrowInterfaceMethods)

			if got := bothWays(t, base, head, off, ss); !slices.Equal(got, []string{"rtest"}) {
				t.Errorf("off selects %v, want the test binary's slice", got)
			}

			if got := bothWays(t, base, head, atLevel2(), ss); len(got) != 0 {
				t.Errorf("on selects %v, want nothing", got)
			}
		})
	}
}
