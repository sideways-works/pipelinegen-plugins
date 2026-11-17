package connector

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// noForks is the kit's check that a fixture shows a change from a fork, as
// a kit without the fixture's no_forks reports it: a plain git server has
// no forks to show.
const noForks = `FAIL  a change from a fork never runs: the connector declares forks, and the fixture names no change from a fork ("forks": [ids])`

// TestTheKit runs the tool's conformance kit on the built connector and its
// fixture: identify, links and changes, every change from the repository
// itself.
func TestTheKit(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "git-connector")

	if out, err := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "../cmd/git-connector").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	out, _ := exec.CommandContext(t.Context(), "go", "tool", "pipelinegen", "plugin", "test", bin, "--tree", "testdata/kit").CombinedOutput() //nolint:errcheck // the lines say what failed

	var failed []string

	for line := range strings.SplitSeq(string(out), "\n") {
		if strings.HasPrefix(line, "FAIL") && line != noForks {
			failed = append(failed, line)
		}
	}

	if len(failed) > 0 || !strings.Contains(string(out), "ok    changes answers") || !strings.Contains(string(out), "ok    no answer or message carries the token") {
		t.Fatalf("the kit:\n%s", out)
	}

	t.Logf("%s", out)
}
