package boundary

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func answer(t *testing.T, dir, ext, body string) {
	t.Helper()

	go func() {
		for range 200 {
			entries, _ := os.ReadDir(dir) //nolint:errcheck // retried
			for _, e := range entries {
				if name, ok := strings.CutSuffix(e.Name(), ".json"); ok {
					// By rename, as the tool answers: a half-written answer would read as an
					// empty one.
					if err := writeAtomic(filepath.Join(dir, name+ext), []byte(body)); err != nil {
						t.Error(err)
					}

					return
				}
			}

			time.Sleep(10 * time.Millisecond)
		}
	}()
}

func TestAMarkTheToolAnswers(t *testing.T) {
	dir := t.TempDir()
	answer(t, dir, ".done", "")

	if err := Mark(dir, "//a:a_test", "a/a_test", Start, 5*time.Second); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, Name("//a:a_test", action(), Start)+".json"))
	if err != nil || string(data) != `{"mark":"start","slice":"a/a_test"}` {
		t.Errorf("request %q %v", data, err)
	}
}

func TestAMarkFailsClosed(t *testing.T) {
	dir := t.TempDir()
	answer(t, dir, ".failed", "the stack's collector found no process")

	if err := Mark(dir, "//a:a_test", "a/a_test", Stop, 5*time.Second); !errors.Is(err, ErrUnmarked) || !strings.Contains(err.Error(), "found no process") {
		t.Errorf("failed: %v", err)
	}

	if err := Mark(t.TempDir(), "//a:a_test", "a/a_test", Start, 200*time.Millisecond); !errors.Is(err, ErrUnmarked) {
		t.Errorf("unanswered: %v", err)
	}
}

// TestMain lets the test binary play the wrapper Bazel runs.
func TestMain(m *testing.M) {
	if os.Getenv("BOUNDARY_HELPER") == "1" {
		os.Exit(Wrap(5*time.Second, []string{"sleep", "30"}))
	}

	os.Exit(m.Run())
}

func TestATestBazelStops(t *testing.T) {
	dir := t.TempDir()

	slices := filepath.Join(t.TempDir(), "slices.json")
	if err := WriteSlices(slices, map[string]string{"//a:a_test": "a/a_test"}); err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^$") //nolint:gosec // this test's own binary

	cmd.Env = append(os.Environ(), "BOUNDARY_HELPER=1", "TEST_TARGET=//a:a_test", EnvDir+"="+dir, EnvSlices+"="+slices)

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	stopped, marks := false, map[string]bool{}

	for range 500 {
		entries, _ := os.ReadDir(dir) //nolint:errcheck // retried
		for _, e := range entries {
			name, ok := strings.CutSuffix(e.Name(), ".json")
			if !ok || marks[name] {
				continue
			}

			marks[name] = true
			if err := writeAtomic(filepath.Join(dir, name+".done"), nil); err != nil {
				t.Fatal(err)
			}
		}

		if len(marks) == 1 && !stopped {
			time.Sleep(200 * time.Millisecond)

			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}

			stopped = true
		}

		if len(marks) == 2 {
			break
		}

		time.Sleep(20 * time.Millisecond)
	}

	err := cmd.Wait()

	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 143 || len(marks) != 2 {
		t.Errorf("exit %v, marks %v", err, marks)
	}
}
