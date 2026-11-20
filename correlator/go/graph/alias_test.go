package graph

import (
	"maps"
	"slices"
	"testing"
)

func payRoot(t *testing.T, decl, method string) string {
	t.Helper()

	root := t.TempDir()

	write(t, root, "pay/go.mod", "module example.com/pay\n\ngo 1.24\n")
	write(t, root, "pay/money/money.go", "package money\n\ntype Amount int\n\n"+decl+"\n")
	write(t, root, "pay/money/str.go", "package money\n\n"+method+"\n")
	write(t, root, "pay/show/show.go", `package show

import (
	"fmt"

	"example.com/pay/money"
)

func Show(a *money.Amount) string {
	if s, ok := any(a).(fmt.Stringer); ok {
		return s.String()
	}

	return fmt.Sprint(int(*a))
}
`)

	return root
}

func walkSelected(t *testing.T, base, head string, on map[string]bool, ss []Slice) []string {
	t.Helper()

	var bound [2][]map[string]string

	for side, root := range []string{base, head} {
		l := loadSide(t, root)

		for _, s := range ss {
			got := units(closure(l.sets, l.tree.roots(s), on)[Executed])

			m := map[string]string{}
			for id := range got {
				m[id] = l.inv.digests[id] + "?"
			}

			bound[side] = append(bound[side], m)
		}
	}

	var out []string

	for i, s := range ss {
		if !maps.Equal(bound[0][i], bound[1][i]) {
			out = append(out, s.Name)
		}
	}

	slices.Sort(out)

	return out
}

func TestAMethodOnAnAliasReachesTheTypesUsers(t *testing.T) {
	cases := map[string]struct{ decl, recv string }{
		"alias":         {"type Cash = Amount", "Cash"},
		"pointer":       {"type Ref = *Amount", "Ref"},
		"chain":         {"type Money = Amount\n\ntype Cash = Money", "Cash"},
		"pointer chain": {"type Ref = *Amount\n\ntype Hold = Ref", "Hold"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			base := payRoot(t, c.decl, "func (c "+c.recv+") String(n int) string { return \"cash\" }")
			head := payRoot(t, c.decl, "func (c "+c.recv+") String() string { return \"cash\" }")

			ss := []Slice{{Suite: "backend", Name: "show", HasList: true, List: []string{"go:pay/show#func:Show"}}}

			for _, on := range levels() {
				if got := walkSelected(t, base, head, on, ss); !slices.Equal(got, []string{"show"}) {
					t.Errorf("links under %v select %v, want [show]", slices.Sorted(maps.Keys(on)), got)
				}

				if got := selectedWith(t, base, head, slices.Sorted(maps.Keys(on)), ss, nil)[Executed]; !slices.Equal(got, []string{"show"}) {
					t.Errorf("bind under %v selects %v, want [show]", slices.Sorted(maps.Keys(on)), got)
				}
			}
		})
	}
}
