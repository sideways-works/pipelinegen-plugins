package graph

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	modver "golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
)

func proxy(t *testing.T, versions map[string]string) string {
	t.Helper()

	dir := t.TempDir()

	for pv, src := range versions {
		p, v, _ := cutAt(pv)
		mod := "module " + p + "\n\ngo 1.24\n"

		srcDir := t.TempDir()
		write(t, srcDir, "go.mod", mod)
		write(t, srcDir, "lib.go", src)

		vdir := filepath.Join(dir, p, "@v")
		write(t, vdir, v+".mod", mod)
		write(t, vdir, v+".info", `{"Version":"`+v+`"}`)

		f, err := os.Create(filepath.Join(vdir, v+".zip"))
		if err != nil {
			t.Fatal(err)
		}

		if err := modzip.CreateFromDir(f, modver.Version{Path: p, Version: v}, srcDir); err != nil {
			t.Fatal(err)
		}

		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}

	return dir
}

func cutAt(pv string) (string, string, bool) {
	for i := len(pv) - 1; i >= 0; i-- {
		if pv[i] == '@' {
			return pv[:i], pv[i+1:], true
		}
	}

	return pv, "", false
}

func withModules(t *testing.T, root, prx, lib, other string) {
	t.Helper()

	edit(t, root, "shop/go.mod", "go 1.24\n", "go 1.24\n\nrequire (\n\texample.com/lib "+lib+"\n\texample.com/other "+other+"\n)\n")
	edit(t, root, "shop/money/money.go", "import \"fmt\"", "import (\n\t\"fmt\"\n\n\t\"example.com/lib\"\n)")
	edit(t, root, "shop/money/money.go", "func Hook() {}", "func Hook() { _ = lib.Name }")

	cmd := exec.CommandContext(t.Context(), "go", "mod", "download", "example.com/lib", "example.com/other")
	cmd.Dir = filepath.Join(root, "shop")

	cmd.Env = append(os.Environ(), "GOPROXY=file://"+prx, "GOSUMDB=off", "GOFLAGS=-mod=mod -modcacherw", "GOTOOLCHAIN=local")

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go mod download: %v\n%s", err, out)
	}
}

func TestModuleBumps(t *testing.T) {
	t.Setenv("GOMODCACHE", t.TempDir())

	prx := proxy(t, map[string]string{
		"example.com/lib@v1.0.0":   "package lib\n\nconst Name = \"one\"\n",
		"example.com/lib@v1.1.0":   "package lib\n\nconst Name = \"two\"\n",
		"example.com/other@v1.0.0": "package other\n",
		"example.com/other@v1.1.0": "package other\n\nconst X = 1\n",
	})

	both := []string{cancel, create}

	cases := map[string]struct {
		lib, other   string
		narrow, wide []string
	}{
		"a bump of an imported module selects the slices linking it": {lib: "v1.1.0", other: "v1.0.0", narrow: both, wide: both},
		"a bump of a module nothing imports selects nothing":         {lib: "v1.0.0", other: "v1.1.0", narrow: nil, wide: both},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			base, head := fixture(t), fixture(t)
			withModules(t, base, prx, "v1.0.0", "v1.0.0")
			withModules(t, head, prx, c.lib, c.other)

			if got := selectedWith(t, base, head, narrowed, shopSlices(), nil)[Executed]; !slices.Equal(got, c.narrow) {
				t.Errorf("on: selects %v, want %v", got, c.narrow)
			}

			if got := selectedWith(t, base, head, without(NarrowModuleBumps), shopSlices(), nil)[Executed]; !slices.Equal(got, c.wide) {
				t.Errorf("off: selects %v, want %v", got, c.wide)
			}
		})
	}
}
