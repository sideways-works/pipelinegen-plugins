package proof

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// shop is the base tree: an order type whose methods no interface names,
// a test that runs Create, and one that only declares an order.
var shop = map[string]string{
	"go.mod": "module example.com/shop\n\ngo 1.24\n",
	"orders/orders.go": `package orders

type Order struct{ Cents int }

func (o Order) Price() int { return o.Cents }

func Create(c int) Order { return Order{Cents: c} }
`,
	"orders/create_test.go": `package orders

import "testing"

func TestCreate(t *testing.T) {
	if Create(1).Price() != 1 {
		t.Fatal("price")
	}
}
`,
	"orders/zero_test.go": `package orders

import "testing"

func TestZero(t *testing.T) {
	var o Order
	if o.Cents != 0 {
		t.Fatal(o)
	}
}
`,
}

// release is the plan the release before made of the change: both order
// tests for a Go reason, a browser spec for a Bazel target.
const release = `{"narrowing": 2, "suites": [
 {"suite": "go", "selected": [
   {"name": "orders/create_test.go", "label": "@deps//slices:orders/create_test.go"},
   {"name": "orders/zero_test.go", "label": "@deps//slices:orders/zero_test.go"}],
  "untouched": [],
  "reasons": {
   "@deps//slices:orders/create_test.go": [{"kind": "executed", "what": "go:orders#func:Create"}],
   "@deps//slices:orders/zero_test.go": [{"kind": "depends", "what": "go:orders#type:Order"}]}},
 {"suite": "web", "selected": [{"name": "web/a.spec.ts", "label": "@deps//slices:web/a.spec.ts"}],
  "untouched": [],
  "reasons": {"@deps//slices:web/a.spec.ts": [{"kind": "target", "what": "//web:bundle"}]}}]}`

// The head changes Create and gives Order a method no interface names. With
// interface-methods on, the test that only declares an order is left out:
// the new method's link from the type's declaration isn't followed, and its
// target is a change of its own.
func TestTheWalkMeasuresANarrowingAgainstTheRelease(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}

	repo := t.TempDir()
	for rel, src := range shop {
		write(t, repo, rel, src)
	}

	git(t, repo, "init", "-q", "-b", "main")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-q", "-m", "base")
	base := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))

	write(t, repo, "orders/orders.go", strings.Replace(shop["orders/orders.go"], "Order{Cents: c}", "Order{Cents: c + 0}", 1))
	write(t, repo, "orders/summary.go", "package orders\n\nfunc (o Order) Summary() string { return \"order\" }\n")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-q", "-m", "head")
	head := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))

	lists := t.TempDir()
	write(t, lists, "go/orders/create_test.go.txt", "go:orders#func:Create\ngo:orders#method:Order.Price\n")
	write(t, lists, "go/orders/zero_test.go.txt", "")
	write(t, lists, "web/web/a.spec.ts.txt", "file:web/a.ts\n")
	write(t, lists, "go/_setup.txt", "go:orders#func:Create\n")

	planFile := filepath.Join(t.TempDir(), "plan.json")
	write(t, filepath.Dir(planFile), "plan.json", release)

	run := func(anyLink bool) *Report {
		t.Helper()

		rep, err := Run(context.Background(), Options{
			Repo: repo, Base: base, Head: head, Lists: lists, Plan: planFile, Work: t.TempDir(), AnyLink: anyLink,
			Cache: t.TempDir(), Explain: []string{"go:orders#typedecl:Order"},
		})
		if err != nil {
			t.Fatal(err)
		}

		return rep
	}

	followed, anyLink := run(false), run(true)

	for _, rep := range []*Report{followed, anyLink} {
		g := suite(t, rep, "go")
		if g.Off != 2 || len(g.OffOnly)+len(g.ReleaseOnly) != 0 || g.UnitsAgree != 2 {
			t.Errorf("with the narrowing off: %+v, want the release's two slices and their units", g)
		}

		if w := suite(t, rep, "web"); w.Kept != 1 || w.Selected != 1 || w.Outside != 1 {
			t.Errorf("web: %+v, want the spec kept for its target", w)
		}
	}

	if g := suite(t, anyLink, "go"); g.Selected != 2 || g.ByCode != 2 {
		t.Errorf("counting any link: %+v, want both slices", g)
	}

	if d := anyLink.Explained["go:orders#typedecl:Order"]; !slices.Contains(d[1], "dependents-only go:orders#method:Order.Summary") {
		t.Errorf("explained %v, want the new method's link from the declaration", d)
	}

	if g := suite(t, followed, "go"); g.Selected != 1 || g.ByCode != 1 {
		t.Errorf("counting followed links: %+v, want the test that runs Create alone", g)
	}

	var out bytes.Buffer

	followed.Write(&out)

	if !strings.Contains(out.String(), "go: 2 slices; the release selected 2 (2 for a Go reason, 0 for a reason outside Go)") {
		t.Errorf("report:\n%s", out.String())
	}
}

func suite(t *testing.T, rep *Report, name string) SuiteReport {
	t.Helper()

	for _, s := range rep.Suites {
		if s.Suite == name {
			return s
		}
	}

	t.Fatalf("no suite %s in %+v", name, rep.Suites)

	return SuiteReport{}
}

func write(t *testing.T, root, rel, src string) {
	t.Helper()

	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir, "-c", "user.name=proof", "-c", "user.email=proof@example.com",
		"-c", "commit.gpgsign=false"}, args...)...)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}

	return string(out)
}
