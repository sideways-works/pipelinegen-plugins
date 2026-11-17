package connector

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheKit runs the tool's conformance kit on the built connector and its
// fixture: every call it declares, against a fake host, and the fork rule.
func TestTheKit(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "github-connector")

	if out, err := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "../cmd/github-connector").CombinedOutput(); err != nil {
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
