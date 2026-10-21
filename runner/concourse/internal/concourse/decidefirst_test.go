package concourse

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func TestAContinuousBuildDecidesBeforeItsPrepare(t *testing.T) {
	cfg, root := fixture(t)

	full := cfg.Runs[runIndex(t, cfg, "nightly")]
	full.Name = "full"
	full.Trigger = plugin.SpecTrigger{Kind: triggerContinuous, Branch: "main"}
	full.WaitMinutes = 30
	cfg.Runs = append(cfg.Runs, full)

	p, err := Generate(cfg, root)
	if err != nil {
		t.Fatal(err)
	}

	plan := text(t, M{"jobs": []any{jobByName(t, p, "plan-full")}})
	for _, want := range []string{"plan --decide --run", "PIPELINEGEN_LIMIT=limits.plan=60", "+ 3600 ))", "PIPELINEGEN_LIMIT: limits.plan=90"} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan-full lacks %q", want)
		}
	}

	if other := text(t, M{"jobs": []any{jobByName(t, p, "plan-main")}}); strings.Contains(other, "--decide") {
		t.Error("a branch run's planning task decides apart")
	}

	for name, c := range map[string]struct {
		decide   string
		prepared bool
		calls    string
	}{
		"superseded": {decide: "mkdir -p handoff && echo '[]' > handoff/gate.json", calls: "plan --decide"},
		"goes on": {decide: "mkdir -p handoff && echo '{}' > handoff/decided.json", prepared: true,
			calls: "plan --decide\nplan --run full deadline=yes limit=limits.plan=60 secret="},
	} {
		t.Run(name, func(t *testing.T) {
			work := t.TempDir()

			for _, d := range []string{"tools", "trigger"} {
				if err := os.MkdirAll(filepath.Join(work, d), 0o750); err != nil {
					t.Fatal(err)
				}
			}

			fake := "#!/bin/sh\nset -e\nif [ \"$2\" = --decide ]; then echo \"plan --decide secret=$PREPARE_SECRET\" >> calls; " + c.decide + "; exit 0; fi\n" +
				"d=no; [ \"$PIPELINEGEN_DEADLINE\" -gt \"$(( $(date +%s) + 3500 ))\" ] && [ \"$PIPELINEGEN_DEADLINE\" -le \"$(( $(date +%s) + 3600 ))\" ] && d=yes\n" +
				"echo \"$1 $2 $3 deadline=$d limit=$PIPELINEGEN_LIMIT secret=$PREPARE_SECRET\" >> calls\n"
			if err := os.WriteFile(filepath.Join(work, "tools", "pipelinegen"), []byte(fake), 0o700); err != nil {
				t.Fatal(err)
			}

			cmd := exec.CommandContext(t.Context(), "sh", "-ec", planLib+continuousPlanFor(60))
			cmd.Dir = work

			cmd.Env = append(os.Environ(), "PIPELINEGEN_RUN=full", "PIPELINEGEN_LIMIT=limits.plan=90",
				"PIPELINEGEN_PLANNER_ENV=PREPARE_SECRET", "PREPARE_SECRET=s3", "PIPELINEGEN_PREPARE=echo prepared > prepared")

			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}

			if _, err := os.Stat(filepath.Join(work, "trigger", "prepared")); (err == nil) != c.prepared {
				t.Fatalf("prepared: %v, want %v", err == nil, c.prepared)
			}

			calls, err := os.ReadFile(filepath.Join(work, "calls"))
			if err != nil {
				t.Fatal(err)
			}

			got := strings.ReplaceAll(strings.TrimSpace(string(calls)), "plan --decide secret=", "plan --decide")
			if got != c.calls {
				t.Fatalf("the tool's calls:\n%s\nwant:\n%s", got, c.calls)
			}
		})
	}
}
