package driver

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestTheKit(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "playwright-driver")

	if out, err := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "../cmd/playwright-driver").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	out, err := exec.CommandContext(t.Context(), "go", "tool", "pipelinegen", "plugin", "test", bin, "--tree", "testdata/web").CombinedOutput()
	if err != nil {
		t.Fatalf("the kit: %v\n%s", err, out)
	}

	t.Logf("%s", out)
}
