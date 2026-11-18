package kid

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestMain is the child when CHILD is set: it runs Answer, then writes its
// coverage into the gocoverdir/ beside the binary, which is all a child
// without GOCOVERDIR can find.
func TestMain(m *testing.M) {
	if os.Getenv("CHILD") != "" {
		Answer()

		exe, _ := os.Executable()
		if dir := filepath.Join(filepath.Dir(exe), "gocoverdir"); testing.CoverMode() != "" {
			if _, err := os.Stat(dir); err == nil {
				os.Args = []string{os.Args[0], "-test.run=^$", "-test.gocoverdir=" + dir}
				m.Run()
			}
		}

		os.Exit(0)
	}

	os.Exit(m.Run())
}

func TestChild(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(exe)
	cmd.Env = []string{"CHILD=1"}

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}
