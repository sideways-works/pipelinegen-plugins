package decls

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestWalk(t *testing.T) {
	root := t.TempDir()

	for _, f := range []string{
		"go.mod", "main.go", "a/a.go", "a/testdata/x.go", "a/vendor/v.go", "a/.hidden/h.go",
		"a/_old/o.go", "b/README.md", "b/c/c.go", "node_modules/n/n.go", "tools/go.mod", "tools/t.go",
	} {
		p := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte("package x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	mods, err := Modules(root)
	if err != nil || !slices.Equal(mods, []string{".", "tools"}) {
		t.Errorf("Modules = %v, %v", mods, err)
	}

	dirs, err := Dirs(root, ".")
	if err != nil || !slices.Equal(dirs, []string{".", "a", "b/c"}) {
		t.Errorf("Dirs = %v, %v", dirs, err)
	}

	dirs, err = Dirs(root, "tools")
	if err != nil || !slices.Equal(dirs, []string{"tools"}) {
		t.Errorf("Dirs(tools) = %v, %v", dirs, err)
	}
}

func TestWalkThroughASymlinkedRoot(t *testing.T) {
	target := t.TempDir()

	for _, f := range []string{"go.mod", "main.go", "a/a.go", "tools/go.mod", "tools/t.go"} {
		p := filepath.Join(target, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte("package x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	link := filepath.Join(t.TempDir(), "source")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	mods, err := Modules(link)
	if err != nil || !slices.Equal(mods, []string{".", "tools"}) {
		t.Errorf("Modules = %v, %v", mods, err)
	}

	dirs, err := Dirs(link, ".")
	if err != nil || !slices.Equal(dirs, []string{".", "a"}) {
		t.Errorf("Dirs = %v, %v", dirs, err)
	}

	dirs, err = Dirs(link, "tools")
	if err != nil || !slices.Equal(dirs, []string{"tools"}) {
		t.Errorf("Dirs(tools) = %v, %v", dirs, err)
	}

	resolved, err := Root(link)
	if err != nil {
		t.Fatal(err)
	}

	want, err := filepath.EvalSymlinks(target)
	if err != nil || resolved != want {
		t.Errorf("Root = %s, want %s (%v)", resolved, want, err)
	}
}
