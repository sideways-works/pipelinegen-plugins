package driver

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

var auditSlices = []plugin.DriverSlice{
	{Name: "audit/matrix_test.go", Sources: []string{"audit/matrix_test.go"}, Tests: []string{"TestMatrix"}},
	{Name: "audit/other_test.go", Sources: []string{"audit/other_test.go"}, Tests: []string{"TestOther"}},
}

func runAudit(t *testing.T, batch, record bool, conf map[string]any, env map[string]string) (map[string]plugin.DriverEvent, string) {
	t.Helper()

	tree := fixture(t)
	req := plugin.DriverRequest{
		Protocol: plugin.Protocol, Work: t.TempDir(), Cache: t.TempDir(), Config: conf,
		Tree: tree, Batch: batch, Record: record, Env: env,
	}

	for _, sl := range auditSlices {
		if record {
			sl.Recording = filepath.Join(t.TempDir(), sl.Name)
		}

		req.Slices = append(req.Slices, sl)
	}

	got := map[string]plugin.DriverEvent{}

	var log bytes.Buffer

	err := run(t.Context(), req, func(ev plugin.DriverEvent) error {
		got[ev.Slice] = ev

		return nil
	}, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}

	return got, log.String()
}

var modes = []struct{ batch, record bool }{{false, false}, {true, false}, {false, true}, {true, true}}

func modeName(batch, record bool) string { return fmt.Sprintf("batch %v, record %v", batch, record) }

func TestASkippedMustRunTestFailsItsFile(t *testing.T) {
	conf := map[string]any{"must_run": map[string]any{"audit/matrix_test.go": "^TestMatrix$"}}

	for _, m := range modes {
		got, log := runAudit(t, m.batch, m.record, conf, nil)

		matrix := got["audit/matrix_test.go"]
		if matrix.Passed || len(matrix.Failures) != 1 || matrix.Failures[0].Test != "TestMatrix" ||
			!strings.Contains(matrix.Failures[0].Tail, "AUDIT_DB is not set") ||
			!strings.Contains(matrix.Failures[0].Tail, "TestMatrix in audit/matrix_test.go skipped, and must_run says it must run: audit/matrix_test.go holds ^TestMatrix$") {
			t.Errorf("%s: the skipped test's file: %+v\n%s", modeName(m.batch, m.record), matrix, log)
		}

		if other := got["audit/other_test.go"]; !other.Passed {
			t.Errorf("%s: the other file: %+v", modeName(m.batch, m.record), other)
		}

		got, _ = runAudit(t, m.batch, m.record, conf, map[string]string{"AUDIT_DB": "here"})
		if matrix := got["audit/matrix_test.go"]; !matrix.Passed {
			t.Errorf("%s: the test ran and passed, its file: %+v", modeName(m.batch, m.record), matrix)
		}
	}
}

func TestAMustRunPatternHoldsOnlyItsFile(t *testing.T) {
	for key, pattern := range map[string]string{"audit/moved_away_test.go": "^TestMatrix$", "audit/other_test.go": "."} {
		conf := map[string]any{"must_run": map[string]any{key: pattern}}

		for _, m := range modes {
			got, log := runAudit(t, m.batch, m.record, conf, nil)

			if matrix := got["audit/matrix_test.go"]; !matrix.Passed {
				t.Errorf("%s: %s held a test of another file: %+v", modeName(m.batch, m.record), key, matrix)
			}

			if key == "audit/moved_away_test.go" && (!strings.Contains(log, "audit/moved_away_test.go") || !strings.Contains(log, "no such file")) {
				t.Errorf("the log doesn't name the missing file:\n%s", log)
			}
		}
	}
}

func TestAFileMustRunSaysHoldsTheTestFailsWithoutIt(t *testing.T) {
	conf := map[string]any{"must_run": map[string]any{"audit/other_test.go": "^TestAudit"}}

	for _, m := range modes {
		got, log := runAudit(t, m.batch, m.record, conf, nil)

		other := got["audit/other_test.go"]
		if other.Passed || len(other.Failures) != 1 || !strings.Contains(other.Failures[0].Tail, "holds no test matching ^TestAudit") {
			t.Errorf("%s: a file without the test it must hold: %+v\n%s", modeName(m.batch, m.record), other, log)
		}

		// A file must_run doesn't name is held only by the patterns.
		if matrix := got["audit/matrix_test.go"]; !matrix.Passed {
			t.Errorf("%s: the file must_run doesn't name: %+v", modeName(m.batch, m.record), matrix)
		}
	}
}

func TestMustRunRefusesABadPattern(t *testing.T) {
	_, err := settingsOf(plugin.DriverRequest{Config: map[string]any{"must_run": map[string]any{"a_test.go": "(unclosed"}}})
	if err == nil || !strings.Contains(err.Error(), "must_run") {
		t.Fatalf("a pattern that doesn't compile: %v", err)
	}
}

func TestMustRunRefusesWhatHoldsNoFile(t *testing.T) {
	for key, pattern := range map[string]string{"audit/matrix.go": "^TestMatrix$", "audit/matrix_test.go": ""} {
		_, err := settingsOf(plugin.DriverRequest{Config: map[string]any{"must_run": map[string]any{key: pattern}}})
		if err == nil || !strings.Contains(err.Error(), "must_run[") {
			t.Errorf("%s: %q: %v", key, pattern, err)
		}
	}
}
