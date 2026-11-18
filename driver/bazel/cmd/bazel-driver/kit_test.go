package main

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A real `bazel test` fetches the platforms repository and the coverage
// tools first, so this drives testdata/fake-bazel; TestTheKitWithBazel uses
// a real Bazel.
func TestTheKit(t *testing.T) {
	fake, err := filepath.Abs(filepath.Join("testdata", "fake-bazel"))
	if err != nil {
		t.Fatal(err)
	}

	kit(t, fake+string(os.PathListSeparator)+os.Getenv("PATH"), filepath.Join(t.TempDir(), "cache"))
}

func TestTheKitWithBazel(t *testing.T) {
	if testing.Short() {
		t.Skip("a real Bazel downloads its dependencies: not under -short")
	}

	if _, err := exec.LookPath("bazel"); err != nil {
		t.Skip("no bazel on PATH")
	}

	cache := filepath.Join(t.TempDir(), "cache")

	// Each Bazel server stays up for hours; stop them, and make the read-only
	// outputs removable for the temporary directory's clean-up.
	t.Cleanup(func() { stopServers(t, filepath.Join(cache, "output_user_root")) })

	kit(t, os.Getenv("PATH"), cache)
}

func kit(t *testing.T, path, cache string) {
	t.Helper()

	bin := filepath.Join(t.TempDir(), "bazel-driver")

	if out, err := exec.CommandContext(t.Context(), "go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	cmd := exec.CommandContext(t.Context(), "go", "tool", "pipelinegen", "plugin", "test", bin,
		"--tree", filepath.Join("testdata", "workspace"), "--cache", cache)

	cmd.Env = append(os.Environ(), "PATH="+path)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the kit: %v\n%s", err, out)
	}

	t.Logf("%s", out)
}

func stopServers(t *testing.T, root string) {
	t.Helper()

	pids, _ := filepath.Glob(filepath.Join(root, "*", "server", "server.pid.txt")) //nolint:errcheck // a pattern without errors

	for _, f := range pids {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}

		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || pid <= 0 {
			continue
		}

		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
			continue
		}

		// A server writes into its output base until it has gone.
		for range 100 {
			if syscall.Kill(pid, 0) != nil {
				break
			}

			time.Sleep(100 * time.Millisecond)
		}
	}

	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error { //nolint:errcheck // best effort before the removal
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o700) //nolint:errcheck,gosec // the test's own Bazel output, made removable
		}

		return nil
	})
}
