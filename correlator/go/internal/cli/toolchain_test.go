package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen-plugins/correlator/go/graph"
	"github.com/sideways-works/pipelinegen-plugins/sbom/go/decls"
)

// fakeGo puts a go first on PATH that answers `go env GOVERSION GOROOT` like
// a version manager's shim, by the nearest .fake-go file above the working
// directory; other commands go to the real go.
func fakeGo(t *testing.T, goroot string) {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), "go", "env", "GOROOT").Output()
	if err != nil {
		t.Fatal(err)
	}

	goBin := filepath.Join(strings.TrimSpace(string(out)), "bin", "go")
	bin := t.TempDir()
	script := `#!/bin/sh
if [ "$*" = "env GOVERSION GOROOT" ]; then
	d=$(pwd -P)
	while [ ! -f "$d/.fake-go" ] && [ "$d" != / ]; do d=$(dirname "$d"); done
	if [ -f "$d/.fake-go" ]; then cat "$d/.fake-go"; else echo go1.30.0; fi
	echo '` + goroot + `'
	exit 0
fi
exec '` + goBin + `' "$@"
`

	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func pick(t *testing.T, dir, version string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, ".fake-go"), []byte(version+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func describeToolchains(t *testing.T) (map[string]string, string) {
	t.Helper()

	code, out, errOut := run(t, "describe", nil)
	if code != exitOK {
		t.Fatalf("describe exit %d: %s", code, errOut)
	}

	var d struct {
		Toolchains map[string]string `json:"toolchains"`
	}

	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatal(err)
	}

	return d.Toolchains, errOut
}

func TestDescribeReportsTheGoThatRuns(t *testing.T) {
	t.Setenv("GOTOOLCHAIN", "go1.99.0")

	cmd := exec.CommandContext(t.Context(), "go", "env", "GOVERSION")
	cmd.Env = graph.Env(decls.Build{})

	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}

	want := strings.TrimSpace(string(out))

	got, _ := describeToolchains(t)
	if !strings.HasPrefix(want, "go") || got["go"] != want {
		t.Errorf("toolchains %v, the go that runs here is %q", got, want)
	}
}

func TestDescribeReportsTheGoOfItsDirectory(t *testing.T) {
	fakeGo(t, "")

	for _, v := range []string{"go1.30.1", "go1.30.2"} {
		dir := t.TempDir()
		pick(t, dir, v)
		t.Chdir(dir)

		if got, _ := describeToolchains(t); got["go"] != v {
			t.Errorf("in a directory picking %s: toolchains %v", v, got)
		}
	}

	t.Chdir(t.TempDir())

	if got, _ := describeToolchains(t); got["go"] != "go1.30.0" {
		t.Errorf("in a directory picking nothing: toolchains %v", got)
	}
}

func TestDescribeWithoutGo(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	code, out, errOut := run(t, "describe", nil)
	if code != exitOK || strings.Contains(out, "toolchains") || !strings.Contains(out, `"needs":["go\u003e=go.mod"]`) {
		t.Errorf("exit %d %s, want an answer that needs go and reports no toolchain", code, out)
	}

	if !strings.Contains(errOut, "no go toolchain to report") {
		t.Errorf("stderr %q says nothing of the missing go", errOut)
	}
}

func TestDescribeGOROOTOfAnotherRelease(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "VERSION"), []byte("go1.29.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	fakeGo(t, root)
	t.Chdir(t.TempDir())

	got, errOut := describeToolchains(t)
	if got != nil || !strings.Contains(errOut, "holds go1.29.0") {
		t.Errorf("toolchains %v, stderr %q", got, errOut)
	}
}

func TestBindNotCachedWhenAModuleResolvesAnotherGo(t *testing.T) {
	fakeGo(t, "")

	cases := []struct {
		module string
		marked bool
		root   string
	}{{"", false, fixture(t)}, {"go1.30.1", true, fixture(t)}}

	for _, c := range cases {
		root := c.root
		if c.module != "" {
			pick(t, filepath.Join(root, "shop"), c.module)
		}

		t.Chdir(t.TempDir())

		code, out, errOut := run(t, "bind", request(t, root, map[string]any{"widths": []string{"executed"}, "slices": []any{}}))
		if code != exitOK || !strings.Contains(out, `"files":["units.jsonl","bind.jsonl","unbound.jsonl"]`) {
			t.Fatalf("module picking %q: exit %d %s %s", c.module, code, out, errOut)
		}

		if got := strings.Contains(out, `"cacheable":false`); got != c.marked {
			t.Errorf("module picking %q: answer %s, marked not cacheable %v, want %v", c.module, out, got, c.marked)
		}

		if c.marked && !strings.Contains(errOut, "the go command in module shop is go1.30.1, in the working directory go1.30.0") {
			t.Errorf("stderr %q doesn't say which go differs", errOut)
		}
	}
}

func TestBindIgnoresAToolchainLine(t *testing.T) {
	root := fixture(t)
	mod := filepath.Join(root, "shop", "go.mod")

	b, err := os.ReadFile(mod)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(mod, append(bytes.Clone(b), []byte("\ntoolchain go1.99.0\n")...), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Chdir(root)

	code, out, errOut := run(t, "bind", request(t, root, map[string]any{"widths": []string{"executed"}, "slices": []any{}}))
	if code != exitOK || strings.Contains(out, "cacheable") {
		t.Errorf("exit %d %s %s, want a cacheable answer", code, out, errOut)
	}
}

func TestBindChecksTheGoOfEachModule(t *testing.T) {
	fakeGo(t, "")

	old := t.TempDir()
	pick(t, old, "go1.20.0")
	t.Setenv("TMPDIR", old)

	root := fixture(t)
	t.Chdir(t.TempDir())

	req := map[string]any{"widths": []string{"executed"}, "slices": []any{}}
	if code, out, errOut := run(t, "bind", request(t, root, req)); code != exitOK {
		t.Fatalf("a module whose own go is new enough: exit %d %s %s", code, out, errOut)
	}

	pick(t, filepath.Join(root, "shop"), "go1.20.0")

	code, out, errOut := run(t, "bind", request(t, root, req))
	if code != exitCannotTell || strings.TrimSpace(out) != `{"cacheable":false}` ||
		!strings.Contains(errOut, "toolchain: shop/go.mod asks for go1.24, the go command run there is go1.20.0") {
		t.Errorf("a module whose own go is too old: exit %d %q %q", code, out, errOut)
	}
}
