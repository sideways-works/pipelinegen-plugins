package collect

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

func put(t *testing.T, root string, files map[string]string) {
	t.Helper()

	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func present(t *testing.T, root string) []string {
	t.Helper()

	var out []string

	err := filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}

		rel, err := filepath.Rel(root, p)
		if err != nil {
			t.Fatal(err)
		}

		out = append(out, filepath.ToSlash(rel))

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	slices.Sort(out)

	return out
}

const prof = "mode: atomic\nexample.com/shop/money/money.go:35.29,39.2 2 1\n"

func TestTakeMovesOnlyGoCoverage(t *testing.T) {
	slice := t.TempDir()
	put(t, slice, map[string]string{
		"a.coverprofile":                     prof,
		"notes.coverprofile":                 "not a profile\n",
		"files.txt":                          "web/app.js\n",
		"result.json":                        "{}",
		"covmeta.aa":                         "meta",
		"gocoverdir/covmeta.bb":              "meta b",
		"gocoverdir/covcounters.bb.1.2":      "counters",
		"gocoverdir/README":                  "kept",
		"deeper/b.coverprofile":              prof,
		"other-plugin/x.coverprofile":        prof,
		"other-plugin/gocoverdir/covmeta.cc": "meta c",
	})

	own := filepath.Join(slice, "go-uses")

	moved, err := Take(slice, own)
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"a.coverprofile", "covmeta.aa", "gocoverdir/covcounters.bb.1.2", "gocoverdir/covmeta.bb"}; !slices.Equal(moved, want) {
		t.Errorf("moved %v, want %v", moved, want)
	}

	want := []string{
		"deeper/b.coverprofile", "files.txt",
		"go-uses/a.coverprofile", "go-uses/gocoverdir/covcounters.bb.1.2", "go-uses/gocoverdir/covmeta.aa", "go-uses/gocoverdir/covmeta.bb",
		"gocoverdir/README", "notes.coverprofile",
		"other-plugin/gocoverdir/covmeta.cc", "other-plugin/x.coverprofile", "result.json",
	}
	if got := present(t, slice); !slices.Equal(got, want) {
		t.Errorf("after: %v\nwant %v", got, want)
	}

	// nothing left to take: a second call moves nothing
	if moved, err := Take(slice, own); err != nil || len(moved) != 0 {
		t.Errorf("again: %v, %v", moved, err)
	}
}

func TestTakeKeepsEveryPart(t *testing.T) {
	slice := t.TempDir()
	own := filepath.Join(slice, "go-uses")

	for _, part := range []map[string]string{
		{"a.coverprofile": prof, "gocoverdir/covmeta.h": "meta", "gocoverdir/covcounters.h.7.100": "one"},
		{"a.coverprofile": prof + "example.com/shop/orders/orders.go:13.30,17.2 2 1\n", "gocoverdir/covmeta.h": "meta", "gocoverdir/covcounters.h.7.100": "two"},
		{"a.coverprofile": "mode: atomic\n", "gocoverdir/covcounters.h.7.100": "three"},
	} {
		put(t, slice, part)

		if _, err := Take(slice, own); err != nil {
			t.Fatal(err)
		}
	}

	want := []string{
		"a.coverprofile", "a~2.coverprofile", "a~3.coverprofile",
		"gocoverdir/covcounters.h.7.100", "gocoverdir/covcounters.h.7.1002", "gocoverdir/covcounters.h.7.1003", "gocoverdir/covmeta.h",
	}
	if got := present(t, own); !slices.Equal(got, want) {
		t.Errorf("own: %v\nwant %v", got, want)
	}

	// go tool covdata reads a counter file only by this shape
	counters := regexp.MustCompile(`^covcounters\.(\S+)\.(\d+)\.(\d+)+$`)
	for _, f := range want {
		if b := filepath.Base(f); filepath.Dir(f) == "gocoverdir" && b != "covmeta.h" && !counters.MatchString(b) {
			t.Errorf("%s isn't a counter file name covdata reads", b)
		}
	}
}

func TestALaterPartTakesFromTheSlicesDirectory(t *testing.T) {
	root := t.TempDir()
	slice := "e2e/shop"
	sliceDir := filepath.Join(root, "e2e", "shop")

	put(t, sliceDir, map[string]string{"gocoverdir/covmeta.h": "meta", "gocoverdir/covcounters.h.7.100": "one"})

	first := filepath.Join(sliceDir, "go")
	if got := SliceDir(first, slice); got != sliceDir {
		t.Fatalf("part 1 takes from %s, want %s", got, sliceDir)
	}

	if _, err := Take(SliceDir(first, slice), first); err != nil {
		t.Fatal(err)
	}

	put(t, sliceDir, map[string]string{"gocoverdir/covmeta.h": "meta", "gocoverdir/covcounters.h.8.200": "two"})

	second := filepath.Join(root, ".pipelinegen", "part-2", "e2e", "shop", "go")
	if got := SliceDir(second, slice); got != sliceDir {
		t.Fatalf("part 2 takes from %s, want %s", got, sliceDir)
	}

	if _, err := Take(SliceDir(second, slice), second); err != nil {
		t.Fatal(err)
	}

	if got, want := present(t, first), []string{"gocoverdir/covcounters.h.7.100", "gocoverdir/covmeta.h"}; !slices.Equal(got, want) {
		t.Errorf("part 1: %v, want %v", got, want)
	}

	if got, want := present(t, second), []string{"gocoverdir/covcounters.h.8.200", "gocoverdir/covmeta.h"}; !slices.Equal(got, want) {
		t.Errorf("part 2: %v, want %v", got, want)
	}

	// a directory of any other shape is its own slice's
	for _, own := range []string{
		filepath.Join(root, ".pipelinegen", "part-1", "e2e", "shop", "go"),
		filepath.Join(root, "other", "part-2", "e2e", "shop", "go"),
		filepath.Join(root, ".pipelinegen", "part-2", "e2e", "cart", "go"),
	} {
		if got := SliceDir(own, slice); got != filepath.Dir(own) {
			t.Errorf("%s: %s, want its parent", own, got)
		}
	}
}
