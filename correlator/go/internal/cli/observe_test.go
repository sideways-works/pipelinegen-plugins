package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func coverage(t *testing.T, lines ...string) string {
	t.Helper()

	dir := t.TempDir()
	text := "mode: set\n" + strings.Join(lines, "\n") + "\n"

	if err := os.WriteFile(filepath.Join(dir, "a.coverprofile"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	return dir
}

func link(t *testing.T, dir string) string {
	t.Helper()

	l := filepath.Join(t.TempDir(), "source")
	if err := os.Symlink(dir, l); err != nil {
		t.Fatal(err)
	}

	return l
}

const (
	grossRan   = "example.com/shop/money/money.go:35.29,39.2 2 1"
	grossIdle  = "example.com/shop/money/money.go:35.29,39.2 2 0"
	foreignRan = "example.com/other/x/x.go:3.14,5.2 1 4"
	stdRan     = "strings/builder.go:40.1,42.2 1 9"
	testSlice  = "shop/orders/create_test.go"
)

func observeOf(t *testing.T, root, slice string, dirs ...string) (int, string, string) {
	t.Helper()

	return run(t, "observe", request(t, root, map[string]any{"slice": slice, "dirs": dirs}))
}

func TestObserveASymlinkedTree(t *testing.T) {
	root := fixture(t)
	cov := coverage(t, grossRan)

	code, out, errOut := observeOf(t, link(t, root), testSlice, cov)
	if code != 0 || strings.TrimSpace(out) != `{"touched":["go:shop/money#func:Gross"]}` {
		t.Errorf("exit %d %s %s", code, out, errOut)
	}
}

func TestObserveCantTellUnattributedBlocks(t *testing.T) {
	root := fixture(t)

	for name, c := range map[string]struct {
		lines []string
		want  string
	}{
		"a file the tree lacks":       {[]string{grossRan, "example.com/shop/money/gone.go:3.1,4.2 1 1"}, "1 covered block"},
		"a line no declaration holds": {[]string{grossRan, "example.com/shop/money/money.go:1.1,1.5 1 2", "example.com/shop/money/money.go:2.1,2.5 1 1"}, "2 covered blocks"},
	} {
		code, out, errOut := observeOf(t, root, testSlice, coverage(t, c.lines...))
		if code != exitCannotTell || !strings.Contains(errOut, c.want) || !strings.Contains(errOut, "example.com/shop/money/") {
			t.Errorf("%s: exit %d %q %q, want %d with %q", name, code, out, errOut, exitCannotTell, c.want)
		}
	}
}

func TestObserveLeavesOutOtherModules(t *testing.T) {
	root := fixture(t)

	code, out, errOut := observeOf(t, root, testSlice, coverage(t, grossRan, foreignRan, stdRan))
	if code != 0 || strings.TrimSpace(out) != `{"touched":["go:shop/money#func:Gross"]}` {
		t.Errorf("with other modules: exit %d %s %s", code, out, errOut)
	}

	// read, the tree's packages, zero counts: the slice ran none of them
	code, out, errOut = observeOf(t, root, testSlice, coverage(t, grossIdle, foreignRan))
	if code != 0 || strings.TrimSpace(out) != `{"touched":[]}` {
		t.Errorf("zero counts in the tree: exit %d %s %s", code, out, errOut)
	}
}

func TestObserveCantTellCoverageOfNoTreePackage(t *testing.T) {
	root := fixture(t)

	for name, c := range map[string]struct {
		slice string
		dirs  []string
	}{
		"other modules ran":                     {testSlice, []string{coverage(t, foreignRan, stdRan)}},
		"a slice that isn't a tree path":        {"x", []string{coverage(t, foreignRan)}},
		"another module path of the tree":       {"x", []string{coverage(t, strings.Replace(grossRan, "example.com/shop/", "example.com/other/", 1))}},
		"other modules, nothing ran":            {"x", []string{coverage(t, "example.com/other/x/x.go:3.14,5.2 1 0")}},
		"the setup ran the tree, not the slice": {testSlice, []string{coverage(t, grossRan), coverage(t, stdRan)}},
	} {
		code, out, errOut := observeOf(t, root, c.slice, c.dirs...)
		if code != exitCannotTell || !strings.Contains(errOut, "none of the tree's modules (example.com/shop)") {
			t.Errorf("%s: exit %d %q %q, want %d naming the tree's module", name, code, out, errOut, exitCannotTell)
		}
	}
}

func TestObserveCantTellATreeWithoutModules(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := observeOf(t, root, "a.go", coverage(t, grossRan))
	if code != exitCannotTell || !strings.Contains(errOut, "no go.mod") {
		t.Errorf("exit %d %q %q, want %d", code, out, errOut, exitCannotTell)
	}
}

func TestBindASymlinkedTreeAndATreeWithoutModules(t *testing.T) {
	root := fixture(t)
	slices := []map[string]any{{"suite": "backend", "name": testSlice, "sources": []string{testSlice}, "list": []string{"go:shop/orders#func:Create"}}}

	answers := make([][]byte, 0, 2)

	for _, tree := range []string{root, link(t, root)} {
		req := request(t, tree, map[string]any{"widths": []string{"executed"}, "slices": slices})

		if code, _, errOut := run(t, "bind", req); code != 0 {
			t.Fatalf("%s: exit %d: %s", tree, code, errOut)
		}

		work, _ := req["work"].(string) //nolint:errcheck // request sets it

		var all []byte

		for _, f := range []string{unitsFile, bindFile, unboundFile} {
			b, err := os.ReadFile(filepath.Join(work, f))
			if err != nil {
				t.Fatal(err)
			}

			all = append(all, b...)
		}

		answers = append(answers, all)
	}

	if !bytes.Equal(answers[0], answers[1]) || !bytes.Contains(answers[1], []byte("go:shop/orders#func:Create")) {
		t.Errorf("bind through the link differs:\n%s\nwant\n%s", answers[1], answers[0])
	}

	empty := t.TempDir()
	if err := os.WriteFile(filepath.Join(empty, "a.go"), []byte("package a\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if code, _, errOut := run(t, "bind", request(t, empty, map[string]any{"widths": []string{"executed"}, "slices": slices})); code != exitCannotTell || !strings.Contains(errOut, "no go.mod") {
		t.Errorf("no module: exit %d %s, want %d", code, errOut, exitCannotTell)
	}
}

func TestDetectOfAMissingTreeFails(t *testing.T) {
	code, out, _ := run(t, "detect", request(t, filepath.Join(t.TempDir(), "gone"), map[string]any{"paths": []string{"shop/go.mod"}}))
	if code != exitFailed {
		t.Errorf("exit %d %s, want %d", code, out, exitFailed)
	}
}

func TestCollectTakesWhatATaskWrote(t *testing.T) {
	t.Setenv("PIPELINEGEN_COVER_DIR", "")

	root := fixture(t)
	slice := filepath.Join(t.TempDir(), "coverage", "shop", "orders", "create_test.go")
	own := filepath.Join(slice, "go-uses")

	bin := filepath.Join(t.TempDir(), "shop")
	build := exec.CommandContext(t.Context(), "go", "build", "-cover", "-coverpkg=./...", "-o", bin, "./cmd/shop")
	build.Dir = filepath.Join(root, "shop")

	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, b)
	}

	// part 1: a coverprofile and a GOCOVERDIR; part 2: the same names again
	for _, lines := range [][]string{{grossRan}, {"example.com/shop/orders/orders.go:25.27,26.25 1 1"}} {
		if err := os.MkdirAll(filepath.Join(slice, "gocoverdir"), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(slice, "a.coverprofile"), []byte("mode: set\n"+strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		cmd := exec.CommandContext(t.Context(), bin)

		cmd.Env = append(os.Environ(), "GOCOVERDIR="+filepath.Join(slice, "gocoverdir"))

		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("shop: %v\n%s", err, b)
		}

		if code, out, errOut := run(t, "collect", request(t, root, map[string]any{"action": "slice-end", "dir": own})); code != 0 {
			t.Fatalf("collect: exit %d %s %s", code, out, errOut)
		}
	}

	if err := os.WriteFile(filepath.Join(slice, "files.txt"), []byte("web/app.js\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := observeOf(t, root, testSlice, own)
	if code != 0 {
		t.Fatalf("observe: exit %d %s", code, errOut)
	}

	for _, id := range []string{"go:shop/money#func:Gross", "go:shop/orders#func:Cancel", "go:shop/cmd/shop#func:main"} {
		if !strings.Contains(out, `"`+id+`"`) {
			t.Errorf("touched %s lacks %s", out, id)
		}
	}

	if _, err := os.Stat(filepath.Join(slice, "files.txt")); err != nil {
		t.Errorf("a file that isn't Go coverage moved: %v", err)
	}
}

func TestObserveLeavesOutAModuleTheConfigExcludes(t *testing.T) {
	root := fixture(t)

	for name, src := range map[string]string{
		"examples/go.mod": "module example.com/examples\n\ngo 1.24\n",
		"examples/x/x.go": "package x\n\nfunc X() int { return 1 }\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	examplesRan := "example.com/examples/x/x.go:3.15,3.30 1 1"
	claimed := []map[string]string{{"path": "shop/go.mod", "role": "manifest"}, {"path": "shop/money/money.go", "role": "source"}}

	req := request(t, root, map[string]any{"slice": "x", "dirs": []string{coverage(t, examplesRan)}, "claimed": claimed})

	code, out, errOut := run(t, "observe", req)
	if code != exitCannotTell || !strings.Contains(errOut, "none of the tree's modules (example.com/shop)") {
		t.Errorf("claimed: exit %d %q %q, want %d naming only example.com/shop", code, out, errOut, exitCannotTell)
	}

	// without claims every go.mod of the tree counts, the excluded one too
	unclaimed := request(t, root, map[string]any{"slice": "x", "dirs": []string{coverage(t, examplesRan)}})
	if code, out, errOut := run(t, "observe", unclaimed); code != exitOK || !strings.Contains(out, "go:examples/x#func:X") {
		t.Errorf("unclaimed: exit %d %q %q, want the examples module read", code, out, errOut)
	}
}

// The local record command doesn't fill the module cache before observing,
// so observe must load no package.
func TestObserveNeedsNoModuleCache(t *testing.T) {
	root := t.TempDir()

	for name, src := range map[string]string{
		"go.mod": "module example.test/fix\n\ngo 1.27\n\nrequire golang.org/x/mod v0.41.0\n",
		"go.sum": "golang.org/x/mod v0.41.0 h1:qJmnOUb4YB+FsEuM3HcWucdZASCPGhsX6uljO6pog0c=\n" +
			"golang.org/x/mod v0.41.0/go.mod h1:Ek9pY8RKWXwsWvd3rQiHYtMqkjSUV+s1Rj7j4H5Ur6o=\n",
		"inner/inner.go": "package inner\n\nimport \"golang.org/x/mod/semver\"\n\n// Newer tells whether a is newer than b.\n" +
			"func Newer(a, b string) bool { return semver.Compare(a, b) > 0 }\n\n// Unused is never called.\nfunc Unused() int { return 1 }\n",
		"inner/inner_test.go": "package inner\n\nimport \"testing\"\n\nfunc TestNewer(t *testing.T) {\n\tif !Newer(\"v1.2.0\", \"v1.1.0\") {\n\t\tt.Fatal(\"not newer\")\n\t}\n}\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// the recording itself is made with the module cache, as a test run is
	bin := filepath.Join(t.TempDir(), "inner.test")
	build := exec.CommandContext(t.Context(), "go", "test", "-c", "-cover", "-coverpkg=./...", "-o", bin, "./inner")
	build.Dir = root

	build.Env = append(os.Environ(), "GOFLAGS=-mod=mod")

	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go test -c: %v\n%s", err, b)
	}

	cov := t.TempDir()
	runTest := exec.CommandContext(t.Context(), bin, "-test.run", "TestNewer", "-test.gocoverdir="+cov)

	if b, err := runTest.CombinedOutput(); err != nil {
		t.Fatalf("inner.test: %v\n%s", err, b)
	}

	_, filled, filledErr := observeOf(t, root, "inner", cov)

	t.Setenv("GOMODCACHE", t.TempDir())
	t.Setenv("GOPROXY", "off")

	code, empty, emptyErr := observeOf(t, root, "inner", cov)
	if code != exitOK || empty != filled || strings.TrimSpace(empty) != `{"touched":["go:inner#func:Newer"]}` {
		t.Errorf("an empty module cache answers exit %d %q %q, a filled one %q %q", code, empty, emptyErr, filled, filledErr)
	}
}

func TestCollectTakesALaterPartIntoItsOwnDirectory(t *testing.T) {
	t.Setenv("PIPELINEGEN_COVER_DIR", "")

	root := fixture(t)
	cov := filepath.Join(t.TempDir(), "coverage")
	slice := filepath.Join(cov, filepath.FromSlash(testSlice))
	parts := []string{
		filepath.Join(slice, "go"),
		filepath.Join(cov, ".pipelinegen", "part-2", filepath.FromSlash(testSlice), "go"),
	}

	for i, line := range []string{grossRan, "example.com/shop/orders/orders.go:25.27,26.25 1 1"} {
		if err := os.MkdirAll(slice, 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(slice, "a.coverprofile"), []byte("mode: set\n"+line+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		req := map[string]any{"action": "slice-end", "slice": testSlice, "dir": parts[i]}
		if code, out, errOut := run(t, "collect", request(t, root, req)); code != 0 {
			t.Fatalf("collect part %d: exit %d %s %s", i+1, code, out, errOut)
		}

		if entries, err := os.ReadDir(parts[i]); err != nil || len(entries) == 0 {
			t.Fatalf("part %d's directory holds nothing: %v", i+1, err)
		}
	}

	code, out, errOut := observeOf(t, root, testSlice, filepath.Join(cov, "_setup", "go"), parts[0], parts[1])
	if code != 0 {
		t.Fatalf("observe: exit %d %s", code, errOut)
	}

	for _, id := range []string{"go:shop/money#func:Gross", "go:shop/orders#func:Cancel"} {
		if !strings.Contains(out, `"`+id+`"`) {
			t.Errorf("touched %s lacks %s", out, id)
		}
	}
}
