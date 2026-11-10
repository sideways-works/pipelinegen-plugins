package observe

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func fixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	if err := os.CopyFS(filepath.Join(root, "shop"), os.DirFS(filepath.Join("..", "graph", "testdata", "shop"))); err != nil {
		t.Fatal(err)
	}

	return root
}

func goTest(t *testing.T, root, test, dir string) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(t.Context(), "go", "test", "-run", test, "-coverpkg=./...",
		"-coverprofile="+filepath.Join(dir, "a.coverprofile"), "./orders")
	cmd.Dir = filepath.Join(root, "shop")

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go test: %v\n%s", err, out)
	}
}

func TestCoverprofile(t *testing.T) {
	root := fixture(t)
	cov := filepath.Join(t.TempDir(), "create")
	goTest(t, root, "TestCreate", cov)

	got, err := Observe(t.Context(), root, "", []string{cov})
	if err != nil {
		t.Fatal(err)
	}

	// an empty function has a block of no statements, which counts when
	// it runs
	want := []string{"go:shop/money#func:Gross", "go:shop/money#func:Hook", "go:shop/money#func:build", "go:shop/orders#func:Create"}
	if !slices.Equal(got.Touched, want) || len(got.SetupLike) != 0 {
		t.Errorf("Observe = %+v, want %v", got, want)
	}
}

func TestGocoverdir(t *testing.T) {
	root := fixture(t)
	cov := t.TempDir()
	bin := filepath.Join(t.TempDir(), "shop")

	build := exec.CommandContext(t.Context(), "go", "build", "-cover", "-coverpkg=./...", "-o", bin, "./cmd/shop")
	build.Dir = filepath.Join(root, "shop")

	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	dir := filepath.Join(cov, "gocoverdir")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}

	run := exec.CommandContext(t.Context(), bin)

	run.Env = append(os.Environ(), "GOCOVERDIR="+dir)

	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("shop: %v\n%s", err, out)
	}

	got, err := Observe(t.Context(), root, "", []string{cov})
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"go:shop/money#func:Gross", "go:shop/orders#func:Create", "go:shop/cmd/shop#func:main", "go:shop/money#method:Amount.String"} {
		if !slices.Contains(got.Touched, id) {
			t.Errorf("touched %v lacks %s", got.Touched, id)
		}
	}

	if slices.Contains(got.Touched, "go:shop/money#func:Unused") {
		t.Error("Unused counted as touched")
	}
}

func TestSetupApart(t *testing.T) {
	root := fixture(t)
	base := t.TempDir()
	own, setup := filepath.Join(base, "orders", "cancel_test.go"), filepath.Join(base, "_setup")

	goTest(t, root, "TestCancel", own)
	goTest(t, root, "TestCreate", setup)

	got, err := Observe(t.Context(), root, "", []string{setup, own})
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"go:shop/orders#func:Cancel", "go:shop/orders#func:Create", "go:shop/money#func:build"} {
		if !slices.Contains(got.Touched, id) {
			t.Errorf("touched %v lacks %s: what the setup ran is the slice's too", got.Touched, id)
		}
	}

	if !slices.Equal(got.SetupLike, []string{"go:shop/money#func:Gross", "go:shop/money#func:Hook", "go:shop/orders#func:Create"}) {
		t.Errorf("setup_like %v, want what only the setup ran", got.SetupLike)
	}

	// a plain test batch takes no setup snapshot
	if _, err := Observe(t.Context(), root, "", []string{filepath.Join(base, "none"), own}); err != nil {
		t.Errorf("no setup: %v", err)
	}

	if _, err := Observe(t.Context(), root, "", []string{setup, filepath.Join(base, "gone")}); !errors.Is(err, ErrMissing) {
		t.Errorf("a missing slice part: %v, want ErrMissing", err)
	}
}

func TestMissingEmptyAndBroken(t *testing.T) {
	root := fixture(t)

	if _, err := Observe(t.Context(), root, "", []string{filepath.Join(t.TempDir(), "gone")}); !errors.Is(err, ErrMissing) {
		t.Errorf("a missing directory: %v, want ErrMissing", err)
	}

	if _, err := Observe(t.Context(), root, "", []string{t.TempDir()}); !errors.Is(err, ErrMissing) {
		t.Errorf("a directory without coverage: %v, want ErrMissing", err)
	}

	zero := t.TempDir()
	if err := os.WriteFile(filepath.Join(zero, "a.coverprofile"), []byte("mode: set\nexample.com/shop/money/money.go:30.29,32.2 1 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got, err := Observe(t.Context(), root, "", []string{zero}); err != nil || len(got.Touched) != 0 {
		t.Errorf("only zero counts: %+v, %v; want an empty answer", got, err)
	}

	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, "a.coverprofile"), []byte("mode: set\nnot a line\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Observe(t.Context(), root, "", []string{bad}); err == nil || errors.Is(err, ErrMissing) {
		t.Errorf("a broken profile: %v, want a failure", err)
	}

	elsewhere := t.TempDir()
	if err := os.WriteFile(filepath.Join(elsewhere, "a.coverprofile"), []byte("mode: set\nexample.com/shop/money/gone.go:3.1,4.2 1 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Observe(t.Context(), root, "", []string{elsewhere}); !errors.Is(err, ErrSource) {
		t.Errorf("coverage of other sources: %v, want ErrSource", err)
	}
}
