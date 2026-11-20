package forge

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheKit(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "github-forge")

	if out, err := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "../cmd/github-forge").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	out, err := exec.CommandContext(t.Context(), "go", "tool", "pipelinegen", "plugin", "test", bin, "--tree", "testdata/kit").CombinedOutput()
	if err != nil {
		t.Fatalf("the kit: %v\n%s", err, out)
	}

	if !strings.Contains(string(out), "ok    a change from a fork never runs") {
		t.Fatalf("the kit never checked the fork rule:\n%s", out)
	}

	t.Logf("%s", out)
}
