package graph

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen-plugins/sbom/go/decls"
	"github.com/sideways-works/pipelinegen-plugins/sbom/go/deps"
)

func fixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	if err := os.CopyFS(filepath.Join(root, "shop"), os.DirFS(filepath.Join("testdata", "shop"))); err != nil {
		t.Fatal(err)
	}

	return root
}

func edit(t *testing.T, root, rel, old, repl string) {
	t.Helper()

	p := filepath.Join(root, rel)

	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(b), old) {
		t.Fatalf("%s holds no %q", rel, old)
	}

	if err := os.WriteFile(p, []byte(strings.Replace(string(b), old, repl, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()

	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// narrowed are the narrowings the selection tests assume; each has a test
// of its own with it off.
var narrowed = []string{NarrowMethodSets, NarrowPureInitialisers, NarrowReferencedNew, NarrowModuleBumps}

const (
	create = "shop/orders/create_test.go"
	cancel = "shop/orders/cancel_test.go"
)

func shopSlices() []Slice {
	return []Slice{
		{
			Suite: "backend", Name: create, HasList: true, Sources: []string{create},
			List: []string{"go:shop/money#func:Gross", "go:shop/money#func:build", "go:shop/orders#func:Create"},
		},
		{
			Suite: "backend", Name: cancel, HasList: true, Sources: []string{cancel},
			List: []string{"go:shop/money#func:Total", "go:shop/money#func:build", "go:shop/orders#func:Cancel"},
		},
	}
}

// loadedSide is a tree as the selection tests compare it: loaded, with its names set, its link
// sets and what go-sbom lists of it.
type loadedSide struct {
	tree *Tree
	sets []LinkSet
	inv  inventory
}

// sides keeps each loaded side by its files' contents: the selection tests ask the same two trees
// under every narrowing level, and many cases' base is the same fixture, while loading one runs
// the go command, which made a test of nine levels take minutes. What a side answers names units
// by their paths in the tree, so two roots of the same files share one. A tree whose files change
// is loaded again.
var sides = map[string]loadedSide{}

func loadSide(t *testing.T, root string) loadedSide {
	t.Helper()

	key := contentKey(t, root)
	if l, ok := sides[key]; ok {
		return l
	}

	tree := load(t, root)
	l := loadedSide{tree: tree, sets: allSets(t, tree), inv: listUnits(t, root)}
	sides[key] = l

	return l
}

// contentKey is a digest of every file under root, its path and its content.
func contentKey(t *testing.T, root string) string {
	t.Helper()

	h := sha256.New()

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		data, err := os.ReadFile(p) //nolint:gosec // a file of the test's own tree
		if err != nil {
			return fmt.Errorf("content key: %w", err)
		}

		rel, _ := filepath.Rel(root, p)                       //nolint:errcheck // under root
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00", rel, len(data)) //nolint:errcheck // a hash never fails to write
		h.Write(data)

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return hex.EncodeToString(h.Sum(nil))
}

func load(t *testing.T, root string) *Tree {
	t.Helper()

	tree, err := Load(t.Context(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}

	return tree
}

// inventory is what go-sbom lists for a tree: each unit's digest and each
// group's units.
type inventory struct {
	digests map[string]string
	groups  map[string][]string
}

func listUnits(t *testing.T, root string) inventory {
	t.Helper()

	inv := inventory{digests: map[string]string{}, groups: map[string][]string{}}

	mods, err := decls.Modules(root)
	if err != nil {
		t.Fatal(err)
	}

	for _, m := range mods {
		dirs, err := decls.Dirs(root, m)
		if err != nil {
			t.Fatal(err)
		}

		for _, dir := range dirs {
			p, err := decls.Read(root, dir)
			if err != nil {
				t.Fatal(err)
			}

			for _, u := range p.Units {
				inv.digests[string(u.ID)] = u.Digest
				inv.groups[dir] = append(inv.groups[dir], string(u.ID))
			}
		}
	}

	g, err := deps.Read(root, mods, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	for _, m := range g.Modules {
		inv.digests[m.ID] = m.Digest
	}

	for _, tl := range g.Tools {
		inv.digests[tl.ID] = tl.Digest
	}

	return inv
}

func resolve(b Binding, inv inventory, unbound map[string][]string) map[string]string {
	out := map[string]string{}

	if b.Everything {
		maps.Copy(out, inv.digests)

		return out
	}

	for id := range b.Units {
		out[id] = inv.digests[id] + "?"
	}

	for _, g := range b.Whole {
		for _, id := range inv.groups[g] {
			out[id] = inv.digests[id]
		}
	}

	if unbound != nil {
		for _, g := range b.Linked {
			out["unbound:"+g] = strings.Join(unbound[g], ",")
		}
	}

	return out
}

func selectedWith(t *testing.T, base, head string, narrowings []string, ss []Slice, setup map[string][]string) [2][]string {
	t.Helper()

	type side struct {
		inv inventory
		a   Answer
	}

	var trees [2]side

	for i, root := range []string{base, head} {
		l := loadSide(t, root)
		trees[i] = side{inv: l.inv, a: l.tree.Bind(Request{Slices: ss, Narrowings: narrowings, Setup: setup})}
	}

	var out [2][]string

	for i, r := range trees[0].a.Results {
		h := trees[1].a.Results[i]

		// as the core reads it: a slice binds the unbound names of every
		// group in its linked
		ub, uh := trees[0].a.Unbound[r.Width], trees[1].a.Unbound[r.Width]

		if changedIn(resolve(r.Binding, trees[0].inv, ub), resolve(h.Binding, trees[1].inv, uh), trees[0].inv, trees[1].inv) {
			out[r.Width] = append(out[r.Width], r.Slice)
		}
	}

	for w := range out {
		slices.Sort(out[w])
	}

	return out
}

func changedIn(a, b map[string]string, ia, ib inventory) bool {
	for _, m := range []map[string]string{a, b} {
		for id := range m {
			if strings.HasPrefix(id, "unbound:") {
				if a[id] != b[id] {
					return true
				}

				continue
			}

			da, oka := ia.digests[id]
			db, okb := ib.digests[id]

			if oka != okb || da != db {
				return true
			}
		}
	}

	return false
}

func selected(t *testing.T, base, head string, ss []Slice) [2][]string {
	t.Helper()

	return selectedWith(t, base, head, narrowed, ss, nil)
}

// change is one edit to the fixture and the slices it must select.
type change struct {
	edit       func(t *testing.T, root string)
	executed   []string
	dependents []string
}

func TestSelection(t *testing.T) {
	both := []string{cancel, create}

	cases := map[string]change{
		"a comment-only edit selects nothing": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "shop/money/money.go", "// Gross adds the tax.", "// Gross adds the tax, rounded down.")
				edit(t, root, "shop/money/money.go", "return Amount{Cents: a.Cents * (100 + Rate) / 100}",
					"// integer cents\n\treturn Amount{\n\t\tCents: a.Cents * (100 + Rate) / 100,\n\t}")
			},
		},
		"an added MarshalJSON selects the slices using the type": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				write(t, root, "shop/money/json.go", "package money\n\nfunc (a Amount) MarshalJSON() ([]byte, error) { return []byte(a.String()), nil }\n")
			},
			executed: both, dependents: both,
		},
		"a changed struct tag selects the slices using the type": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "shop/money/money.go", "`json:\"cents\"`", "`json:\"c\"`")
			},
			executed: both, dependents: both,
		},
		"a changed constant selects the slices reaching it": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "shop/money/money.go", "const Rate = 19", "const Rate = 20")
			},
			executed: []string{create}, dependents: []string{create},
		},
		"a changed initialiser without effects selects the slices using the variable": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "shop/money/money.go", `"EUR")`, `"CHF")`)
			},
			executed: []string{create}, dependents: []string{create},
		},
		"a changed initialiser that calls a function selects every slice linking the package": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "shop/money/money.go", "var Table = build()", "var Table = map[string]int{\"eur\": 100}")
			},
			executed: both, dependents: both,
		},
		"an added init selects every slice linking the package": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				write(t, root, "shop/money/init.go", "package money\n\nfunc init() { Table[\"chf\"] = 100 }\n")
			},
			executed: both, dependents: both,
		},
		"a body added to an empty function selects its callers' slices": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "shop/money/money.go", "func Hook() {}", "func Hook() { Table[\"x\"] = 1 }")
			},
			executed: []string{create}, dependents: []string{create},
		},
		"a changed declaration no slice reaches selects nothing": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "shop/money/money.go", "return Rate * 2", "return Rate * 3")
			},
		},
		"a changed method no slice ran selects only the dependents width": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "shop/orders/orders.go", "return o.Total.Cents", "return o.Total.Cents + 1")
			},
			dependents: both,
		},
		"a new file with a declaration nothing reaches selects every slice linking it": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				write(t, root, "shop/money/extra.go", "package money\n\nfunc Extra() int { return 1 }\n")
			},
			executed: both, dependents: both,
		},
		"a new helper called by a changed function selects through that function": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				write(t, root, "shop/money/net.go", "package money\n\nfunc Net(a Amount) Amount { return a }\n")
				edit(t, root, "shop/orders/orders.go", "money.Hook()", "money.Hook()\n\t_ = money.Net(money.Amount{})")
			},
			executed: []string{create}, dependents: []string{create},
		},
		"a new package no slice links selects nothing": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				write(t, root, "shop/tax/tax.go", "package tax\n\nfunc Rate() int { return 7 }\n")
			},
		},
		// the go directive is a toolchain unit, which the core samples
		// with: nothing binds it
		"a changed go directive selects nothing by binding": {
			edit: func(t *testing.T, root string) {
				t.Helper()
				edit(t, root, "shop/go.mod", "go 1.24", "go 1.25")
			},
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			base, head := fixture(t), fixture(t)
			c.edit(t, head)

			got := selected(t, base, head, shopSlices())

			for w, want := range [][]string{c.executed, c.dependents} {
				if !slices.Equal(got[w], want) {
					t.Errorf("width %s selects %v, want %v", Width(w), got[w], want)
				}
			}
		})
	}
}

func TestSliceWithoutListIsBoundToEverything(t *testing.T) {
	ss := shopSlices()
	ss[1].HasList, ss[1].List = false, nil

	base, head := fixture(t), fixture(t)
	edit(t, head, "shop/money/money.go", "return Rate * 2", "return Rate * 3")

	if got := selected(t, base, head, ss)[Executed]; !slices.Equal(got, []string{cancel}) {
		t.Errorf("selects %v, want only the slice without a list", got)
	}
}

func TestTestsByName(t *testing.T) {
	root := fixture(t)
	write(t, root, "shop/orders/both_test.go",
		"package orders\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) { Create(1) }\n\nfunc TestB(t *testing.T) { Cancel(Order{}) }\n")

	s := Slice{Suite: "backend", Name: "a", Sources: []string{"shop/orders/both_test.go"}, Tests: []string{"TestA"}, HasList: true}
	a := load(t, root).Bind(Request{Slices: []Slice{s}, Narrowings: narrowed})

	units := a.Results[0].Binding.Units
	if _, ok := units["go:shop/orders#func:TestA@test"]; !ok {
		t.Errorf("TestA isn't bound: %v", slices.Sorted(maps.Keys(units)))
	}

	if _, ok := units["go:shop/orders#func:TestB@test"]; ok {
		t.Error("TestB is bound, though the slice runs TestA only")
	}
}

func TestEveryBoundUnitIsListed(t *testing.T) {
	root := fixture(t)
	write(t, root, "shop/orders/helper_test.go", "package orders_test\n\nvar helperTable = map[string]int{}\n")

	inv := listUnits(t, root)

	ss := append(shopSlices(), Slice{Suite: "backend", Name: "x", Sources: []string{"shop/orders/helper_test.go"}, HasList: true})
	a := load(t, root).Bind(Request{Slices: ss})

	for _, r := range a.Results {
		for id, why := range r.Binding.Units {
			if _, ok := inv.digests[id]; !ok {
				t.Errorf("%s binds %s, which go-sbom doesn't list", r.Slice, id)
			}

			if why.Via != "" && strings.HasPrefix(why.Via, "go:") {
				if _, ok := inv.digests[why.Via]; !ok {
					t.Errorf("%s: reason via %s, which go-sbom doesn't list", id, why.Via)
				}
			}
		}

		for _, g := range r.Binding.Whole {
			if len(inv.groups[g]) == 0 {
				t.Errorf("%s binds the whole of %s, which go-sbom doesn't list", r.Slice, g)
			}
		}
	}

	for _, names := range a.Unbound[Executed] {
		for _, id := range names {
			if _, ok := inv.digests[id]; !ok {
				t.Errorf("unbound %s isn't listed", id)
			}
		}
	}
}

func TestWhy(t *testing.T) {
	tree := load(t, fixture(t))

	got := tree.Why("go:shop/orders#func:Create", "go:shop/money#const:Rate")
	want := []Step{{Unit: "go:shop/orders#func:Create"}, {Unit: "go:shop/money#func:Gross", Note: "referenced"}, {Unit: "go:shop/money#const:Rate", Note: "referenced"}}

	if !slices.Equal(got, want) {
		t.Errorf("Why = %+v, want %+v", got, want)
	}

	if got := tree.Why("go:shop/orders#func:Cancel", "go:shop/orders#method:Order.Price"); len(got) != 3 || got[2].Note != "a method of the type" {
		t.Errorf("Why through the method set = %+v", got)
	}

	if got := tree.Why("go:shop/money#func:Unused", "go:shop/orders#func:Create"); got != nil {
		t.Errorf("Why with no path = %+v", got)
	}
}

func TestVias(t *testing.T) {
	a := load(t, fixture(t)).Bind(Request{Slices: shopSlices()[:1], Narrowings: narrowed})
	b := a.Results[0].Binding

	if got := b.Vias["go:shop/money#const:Rate"]; !slices.Equal(got, []string{"go:shop/money#func:Gross"}) {
		t.Errorf("Rate's vias %v", got)
	}

	amount := b.Vias["go:shop/money#type:Amount"]
	for _, want := range []string{"go:shop/money#func:Gross", "go:shop/orders#func:Create", "go:shop/orders#type:Order"} {
		if !slices.Contains(amount, want) {
			t.Errorf("Amount's vias %v lack %s", amount, want)
		}
	}
}
