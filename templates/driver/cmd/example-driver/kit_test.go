package main

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// TestTheKit runs pipelinegen's conformance kit, at the version go.mod requires, on the built
// driver: every slice of its fixture one by one and bundled, recording and not.
func TestTheKit(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "example-driver")

	if out, err := exec.CommandContext(t.Context(), "go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	out, err := exec.CommandContext(t.Context(), "go", "tool", "pipelinegen", "plugin", "test", bin, "--tree", "../../testdata/kit").CombinedOutput()
	if err != nil {
		t.Fatalf("the kit: %v\n%s", err, out)
	}

	t.Logf("%s", out)
}
