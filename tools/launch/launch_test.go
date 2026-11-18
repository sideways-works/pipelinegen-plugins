package launch

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestChildEnvSeesNothingOfBazelRun(t *testing.T) {
	env := ChildEnv([]string{
		"BUILD_WORKSPACE_DIRECTORY=/w", "RUNFILES_DIR=/r", "TEST_SRCDIR=/s", "TEST_TMPDIR=/t",
		"GOROOT=/other", "GOWORK=/w/go.work", "GOFLAGS=-mod=mod", "PATH=/usr/bin", "HOME=/h",
	}, "/sdk/bin", "/node/bin")

	want := []string{"TEST_TMPDIR=/t", "HOME=/h", "PATH=/sdk/bin:/node/bin:/usr/bin", "GOTOOLCHAIN=local", "GOWORK=off", "GOFLAGS=-mod=mod -trimpath"}
	if !slices.Equal(env, want) {
		t.Fatalf("got %q, want %q", env, want)
	}
}

func TestChildEnvKeepsTrimpathOnce(t *testing.T) {
	env := ChildEnv([]string{"GOFLAGS=-trimpath"})
	if !slices.Contains(env, "GOFLAGS=-trimpath") {
		t.Fatalf("got %q", env)
	}
}

func TestCheckVersions(t *testing.T) {
	versions := "golang 1.27.1\ngolangci-lint 2.14.0\n# a comment\nnodejs 26.10.0\n"

	if err := CheckVersions(versions, map[string]string{"golang": "1.27.1", "nodejs": "26.10.0"}); err != nil {
		t.Fatal(err)
	}

	err := CheckVersions(versions, map[string]string{"golang": "1.27.2", "golangci-lint": "2.14.0"})
	if err == nil || !strings.Contains(err.Error(), `golang "1.27.1", MODULE.bazel pins 1.27.2`) {
		t.Fatalf("got %v", err)
	}
}

func write(t *testing.T, root string, files ...string) {
	t.Helper()

	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUnitsAndModules(t *testing.T) {
	root := t.TempDir()
	write(t, root,
		"driver/go/go.mod", "correlator/react/package.json", "correlator/go/go.mod",
		"correlator/go/cover/go.mod", "correlator/go/collect/testdata/server/go.mod",
		"templates/sbom/go.mod", "docs/x/go.mod", "sbom/empty/README.md", "tools/go.mod",
	)

	units, err := Units(root)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"correlator/go", "correlator/react", "driver/go", "templates/sbom", "tools"}
	if !slices.Equal(units, want) {
		t.Fatalf("units: got %q, want %q", units, want)
	}

	mods, err := Modules(filepath.Join(root, "correlator", "go"))
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(mods, []string{".", "cover"}) {
		t.Fatalf("modules: got %q", mods)
	}
}
