package concourse

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func TestAContinuousRun(t *testing.T) {
	cfg, root := fixture(t)

	full := cfg.Runs[runIndex(t, cfg, "nightly")]
	full.Name = "full"
	full.Trigger = plugin.SpecTrigger{Kind: triggerContinuous, Branch: "main"}
	cfg.Runs = append(cfg.Runs, full)

	p, err := Generate(cfg, root)
	if err != nil {
		t.Fatal(err)
	}

	// its builds run side by side, each deciding from the run's records
	plan := jobByName(t, p, "plan-full")
	if s := text(t, M{"jobs": []any{plan}}); plan["serial"] != nil || plan["max_in_flight"] != nil || strings.Contains(s, "SERIAL") {
		t.Errorf("the planning job is serial, or says so:\n%s", s)
	}

	if s := text(t, M{"jobs": []any{plan}}); !strings.Contains(s, "version: every") || !strings.Contains(s, "trigger: true") {
		t.Errorf("the planning job doesn't take every commit:\n%s", s)
	}

	suite := text(t, M{"jobs": []any{jobByName(t, p, "full-e2e")}})

	for _, want := range []string{"version: every", "passed:\n", "- plan-full"} {
		if !strings.Contains(suite, want) {
			t.Errorf("full-e2e lacks %q", want)
		}
	}

	for _, unwanted := range []string{"max_in_flight: 1", "serial"} {
		if strings.Contains(suite, unwanted) {
			t.Errorf("full-e2e has %q", unwanted)
		}
	}

	if triggering(t, plan) != 1 {
		t.Error("the planning job triggers more than once")
	}

	planText := text(t, M{"jobs": []any{plan}})
	for _, want := range []string{"--gate handoff", "load_var: planned", "file: handoff/gate.json", "values: ((.:planned))", "put: planned-full", "file: handoff/version"} {
		if !strings.Contains(planText, want) {
			t.Errorf("plan-full lacks %q:\n%s", want, planText)
		}
	}

	if !strings.Contains(text(t, p), "name: planned-full\n") {
		t.Error("no planned-full resource")
	}

	e2eJob := jobByName(t, p, "full-e2e")
	if triggering(t, e2eJob) != 1 || !strings.Contains(suite, "get: handoff\n") || !strings.Contains(suite, "resource: planned-full\n") {
		t.Errorf("full-e2e doesn't follow the planned hand-off alone:\n%s", suite)
	}

	if fly, err := exec.LookPath("fly"); err == nil {
		file := filepath.Join(t.TempDir(), "p.yml")
		if err := os.WriteFile(file, []byte(text(t, p)), 0o600); err != nil {
			t.Fatal(err)
		}

		if out, err := exec.CommandContext(t.Context(), fly, "validate-pipeline", "--strict", "-c", file, "-v", "github_token=x").CombinedOutput(); err != nil {
			t.Fatalf("fly: %v\n%s", err, out)
		}
	}

	if d := describe(); !slices.Contains(d.Capabilities, capContinuous) || slices.Contains(d.Capabilities, "serial_jobs") {
		t.Fatalf("describe: %+v", d)
	}
}
