package concourse

import (
	"os"
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
	for _, want := range []string{"- continuous-plan\n", "- --plan-minutes\n", "- \"60\"\n", "PIPELINEGEN_LIMIT: limits.plan=90"} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan-full lacks %q", want)
		}
	}

	if other := text(t, M{"jobs": []any{jobByName(t, p, "plan-main")}}); strings.Contains(other, "continuous-plan") {
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

			mustStep(t, work, append(os.Environ(), "PIPELINEGEN_RUN=full", "PIPELINEGEN_LIMIT=limits.plan=90",
				"PIPELINEGEN_PLANNER_ENV=PREPARE_SECRET", "PREPARE_SECRET=s3", "PIPELINEGEN_PREPARE=echo prepared > prepared"),
				"continuous-plan", "--plan-minutes", "60")

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
