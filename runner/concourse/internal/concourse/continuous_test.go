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

	e2e := &cfg.Suites[suiteIndex(t, cfg, "e2e")]
	e2e.Lock = &plugin.SpecLock{Name: "browsers", Minutes: 90}

	full := cfg.Runs[runIndex(t, cfg, "nightly")]
	full.Name = "full"
	full.Trigger = plugin.SpecTrigger{Kind: triggerContinuous, Branch: "main"}
	cfg.Runs = append(cfg.Runs, full)

	p, err := Generate(cfg, root)
	if err != nil {
		t.Fatal(err)
	}

	if plan := jobByName(t, p, "plan-full"); plan["serial"] != nil || strings.Contains(text(t, M{"jobs": []any{plan}}), plugin.EnvSerialPlan) {
		t.Errorf("the planning job is serial, or says so, though the spec doesn't ask: %v", plan["serial"])
	}

	cfg.Runs[len(cfg.Runs)-1].SerialPlan = true

	if p, err = Generate(cfg, root); err != nil {
		t.Fatal(err)
	}

	plan := jobByName(t, p, "plan-full")
	if plan["serial"] != true || !strings.Contains(text(t, M{"jobs": []any{plan}}), plugin.EnvSerialPlan+": \"1\"") {
		t.Errorf("the planning job the spec asks to be serial isn't, or doesn't tell its plan step: %v", plan["serial"])
	}

	if s := text(t, M{"jobs": []any{plan}}); !strings.Contains(s, "version: every") || !strings.Contains(s, "trigger: true") {
		t.Errorf("the planning job doesn't take every commit:\n%s", s)
	}

	suite := text(t, M{"jobs": []any{jobByName(t, p, "full-e2e")}})

	for _, want := range []string{"version: every", "passed:\n", "- plan-full", "timeout: 90m"} {
		if !strings.Contains(suite, want) {
			t.Errorf("full-e2e lacks %q", want)
		}
	}

	for _, unwanted := range []string{"max_in_flight: 1", "serial", "task: lock\n"} {
		if strings.Contains(suite, unwanted) {
			t.Errorf("full-e2e has %q", unwanted)
		}
	}

	if !strings.Contains(suite, "PIPELINEGEN_LOCK: browsers") {
		t.Error("a continuous run's suite doesn't take its lock as the config says")
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

	if d := describe(); !slices.Contains(d.Capabilities, capContinuous) || !slices.Contains(d.Capabilities, plugin.CapSerialJobs) {
		t.Fatalf("describe: %+v", d)
	}
}

func TestALockPerBatchInEveryRun(t *testing.T) {
	cfg, root := fixture(t)

	e2e := &cfg.Suites[suiteIndex(t, cfg, "e2e")]
	e2e.Lock = &plugin.SpecLock{Name: "browsers", Minutes: 60, Slots: 2, Per: lockPerBatch}

	p, err := Generate(cfg, root)
	if err != nil {
		t.Fatal(err)
	}

	for _, run := range []string{"main", "nightly", "pr"} {
		s := text(t, M{"jobs": []any{jobByName(t, p, run+"-e2e")}})

		for _, want := range []string{"task: lock\n", "- acquire\n", "task: unlock\n", "- release\n", "PIPELINEGEN_BATCH: ((.:batch))", "timeout: 60m"} {
			if !strings.Contains(s, want) {
				t.Errorf("%s-e2e lacks %q", run, want)
			}
		}

		if strings.Contains(s, "PIPELINEGEN_LOCK: browsers") {
			t.Errorf("%s-e2e takes the lock for the whole build", run)
		}
	}

	if d := describe(); !slices.Contains(d.Capabilities, plugin.CapLockPerBatch) {
		t.Fatalf("describe: %+v", d)
	}
}
