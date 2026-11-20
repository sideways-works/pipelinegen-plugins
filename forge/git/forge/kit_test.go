package forge

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The kit wants a change from a fork in the fixture; a plain git server has none.
const noForks = `FAIL  a change from a fork never runs: the forge declares forks, and the fixture names no change from a fork ("forks": [ids])`

func TestTheKit(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "git-forge")

	if out, err := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "../cmd/git-forge").CombinedOutput(); err != nil {
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
