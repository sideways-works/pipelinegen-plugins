package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sideways-works/pipelinegen-sbom-go/decls"
	"github.com/sideways-works/pipelinegen/plugin"
)

func TestEntriesAndTwins(t *testing.T) {
	root := t.TempDir()

	for rel, src := range map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.24\n",
		"p/p.go": "package p\n\nfunc Run() int { return 1 }\n",
		"p/p_test.go": "package p\n\nimport \"testing\"\n\n" +
			"type env struct{}\n\nfunc (env) Get() int { return 1 }\n\ntype plain struct{}\n\n" +
			"func TestMain(m *testing.M) { m.Run() }\n\nfunc TestRun(t *testing.T) { _ = Run() }\n\n" +
			"func BenchmarkRun(b *testing.B) {}\n\nfunc FuzzRun(f *testing.F) {}\n\nfunc ExampleRun() {}\n\nfunc helper() {}\n",
		"p/x_test.go": "package p_test\n\nimport \"testing\"\n\nfunc TestOut(t *testing.T) {}\n",
	} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	s, err := packageSet(root, "p", decls.Config{})
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]Unit{}
	for _, u := range s.Units {
		got[u.ID] = u
	}

	entries := map[string]string{
		"go:p#func:TestRun@test": "TestRun", "go:p#func:BenchmarkRun@test": "BenchmarkRun", "go:p#func:FuzzRun@test": "FuzzRun",
		"go:p#func:ExampleRun@test": "ExampleRun", "go:p#func:TestOut@xtest": "TestOut",
		"go:p#func:TestMain@test": "", "go:p#func:helper@test": "", "go:p#func:Run": "",
	}

	for id, want := range entries {
		u, ok := got[id]
		if !ok {
			t.Errorf("no unit %s in %v", id, s.Units)

			continue
		}

		if u.Entry != want {
			t.Errorf("%s: entry %q, want %q", id, u.Entry, want)
		}
	}

	roots := map[string]bool{"go:p#type:env@test": false, "go:p#typedecl:env@test": true, "go:p#type:plain@test": true}

	for id, root := range roots {
		u, ok := got[id]
		if !ok {
			t.Errorf("no unit %s", id)

			continue
		}

		if u.NotRoot == root {
			t.Errorf("%s: not_root %v", id, u.NotRoot)
		}
	}
}

func TestPrivateMethodsLinkWhileTheNarrowingIsOff(t *testing.T) {
	root := t.TempDir()

	for rel, src := range map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.24\n",
		"p/p.go": "package p\n\ntype T struct{ n int }\n\nfunc (t T) N() int { return t.half() * 2 }\n\nfunc (t T) half() int { return t.n / 2 }\n",
	} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	s, err := packageSet(root, "p", decls.Config{})
	if err != nil {
		t.Fatal(err)
	}

	want := Link{From: "go:p#type:T", To: "go:p#methodsig:T.half", Kind: plugin.LinkUses, Narrowing: NarrowUnexportedMethods, While: plugin.WhileOff}
	if len(s.Links) != 1 || s.Links[0] != want {
		t.Errorf("links %+v, want %+v", s.Links, want)
	}

	for _, u := range s.Units {
		if u.ID == "go:p#type:T" && u.NotRoot {
			t.Error("a package type is no root")
		}
	}
}
