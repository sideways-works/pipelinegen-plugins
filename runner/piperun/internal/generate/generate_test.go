package generate

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

var update = flag.Bool("update", false, "write the golden file anew")

func fixture(t *testing.T) (plugin.Spec, Settings) {
	t.Helper()

	var s plugin.Spec

	data, err := os.ReadFile("testdata/spec.json")
	if err != nil {
		t.Fatal(err)
	}

	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}

	var config map[string]any

	data, err = os.ReadFile("testdata/config.json")
	if err != nil {
		t.Fatal(err)
	}

	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}

	set, err := ReadSettings(config)
	if err != nil {
		t.Fatal(err)
	}

	return s, set
}

func generated(t *testing.T, s plugin.Spec, set Settings) (File, []byte) {
	t.Helper()

	f, _, err := Generate(s, set)
	if err != nil {
		t.Fatal(err)
	}

	text, err := Text(f)
	if err != nil {
		t.Fatal(err)
	}

	return f, text
}

func TestThePipelineIsTheGoldenOne(t *testing.T) {
	s, set := fixture(t)
	_, text := generated(t, s, set)

	golden := filepath.Join("testdata", "piperun.golden.yaml")

	if *update {
		if err := os.WriteFile(golden, text, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(text, want) {
		t.Fatalf("the pipeline differs from %s; go test -update writes it anew", golden)
	}

	if _, again := generated(t, s, set); !bytes.Equal(text, again) {
		t.Fatal("the same spec gave other bytes")
	}
}

// steps are every step of a pipeline's jobs and templates, with where it is.
func steps(p Pipeline) map[string]Step {
	out := map[string]Step{}

	add := func(where string, j Job) {
		for i, st := range j.Steps {
			out[where+"/"+string(rune('0'+i))] = st
		}

		for i, st := range j.Finally {
			out[where+"/finally"+string(rune('0'+i))] = st
		}

		if j.Sidecar != nil {
			out[where+"/sidecar"] = *j.Sidecar
		}
	}

	for n, j := range p.Jobs {
		add(n, j)
	}

	for n, j := range p.Templates {
		add(n, j)
	}

	return out
}

func TestNoStepThatRunsTheChangesCodeHoldsAWritingToken(t *testing.T) {
	s, set := fixture(t)
	f, _ := generated(t, s, set)

	for run, p := range f.Pipelines {
		for where, st := range steps(p) {
			writes := false

			for _, sec := range st.Secrets {
				writes = writes || f.Secrets[sec].Writes
			}

			own := len(st.Run) > 1 && st.Run[0] == tool
			changes := !own || slices.Contains(st.Run, "--out") || (len(st.Run) > 2 && st.Run[1] == "slice")

			switch {
			case writes && changes:
				t.Errorf("%s %s runs the change's code holding a writing token: %v", run, where, st.Secrets)
			case writes && st.Trust != "pinned":
				t.Errorf("%s %s holds a writing token unpinned", run, where)
			}
		}
	}
}

func TestEveryJobHasATimeLimitAndEveryOwnStepTheMark(t *testing.T) {
	s, set := fixture(t)
	f, _ := generated(t, s, set)

	for run, p := range f.Pipelines {
		for n, j := range p.Jobs {
			if j.Minutes <= 0 {
				t.Errorf("%s job %s has no time limit", run, n)
			}
		}

		for n, j := range p.Templates {
			if j.Minutes <= 0 {
				t.Errorf("%s template %s has no time limit", run, n)
			}
		}

		for where, st := range steps(p) {
			if len(st.Run) > 0 && st.Run[0] == tool && st.Env[envStep] != "1" {
				t.Errorf("%s %s runs the tool without %s", run, where, envStep)
			}
		}
	}
}

func TestThePlanEmitsEveryTemplateOfItsRun(t *testing.T) {
	s, set := fixture(t)
	f, _ := generated(t, s, set)

	main := f.Pipelines["main"]
	want := []string{"go-end", "go-slices", "web-end", "web-prepare", "web-slices"}

	if got := main.Jobs[jobWrite].Emits; !slices.Equal(got, want) {
		t.Fatalf("main emits %v, want %v", got, want)
	}

	if got := f.Pipelines["nightly"].Jobs[jobWrite].Emits; !slices.Equal(got, []string{"go-end", "go-slices"}) {
		t.Fatalf("nightly emits %v", got)
	}

	// the pool waits for the prepare, the end for both however they ended
	if a := main.Templates["web-slices"].After; len(a) != 1 || a[0].Job != "web-prepare" {
		t.Fatalf("web-slices after %v", a)
	}

	if a := main.Templates["web-end"].After; len(a) != 2 || !a[0].Ended || !a[1].Ended {
		t.Fatalf("web-end after %v", a)
	}
}

func TestATimerIsACronAtItsStartWithinItsWindow(t *testing.T) {
	s, set := fixture(t)
	f, _ := generated(t, s, set)

	trig := f.Pipelines["nightly"].Triggers[0]
	if trig.Uses != "cron" || trig.With["schedule"] != "0 3 * * *" || trig.With["window"] != "60m" || trig.With["location"] != "Europe/Berlin" {
		t.Fatalf("nightly trigger %+v", trig)
	}

	if c := f.Pipelines["main"].Triggers[0]; c.Uses != "git" || c.With["mode"] != "continuous" {
		t.Fatalf("main trigger %+v", c)
	}
}

func TestWhatPiperunCantRunIsRefusedByName(t *testing.T) {
	for name, change := range map[string]func(*plugin.Spec){
		"a task file suite": func(s *plugin.Spec) { s.Suites[0].Steps = nil; s.Suites[0].RunSlice = "ci/go.yml" },
		"a pull request run": func(s *plugin.Spec) {
			s.Runs[0].Trigger = plugin.SpecTrigger{Kind: plugin.TriggerPullRequests, Base: "main"}
		},
		"pipelinegen by commit": func(s *plugin.Spec) { s.Tools[0].Version, s.Tools[0].Commit = "", strings.Repeat("a", 40) },
		"an unknown image":      func(s *plugin.Spec) { s.Suites[0].Image = "rust" },
		"a whole-suite check": func(s *plugin.Spec) {
			s.Suites[0].WholeSuite = []plugin.SpecWholeSuite{{Name: "cover", Task: "ci/cover.yml"}}
		},
	} {
		s, set := fixture(t)
		change(&s)

		if _, _, err := Generate(s, set); !errors.Is(err, ErrSpec) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestTheConfigsPinOfPipelinegenWins(t *testing.T) {
	s, set := fixture(t)
	s.Tools = nil

	if _, _, err := Generate(s, set); !errors.Is(err, ErrSpec) {
		t.Fatalf("no pin at all: %v", err)
	}

	set.Pipelinegen = &ToolPin{Version: "v0.9.1", SHA256: strings.Repeat("a", 64)}

	f, _, err := Generate(s, set)
	if err != nil {
		t.Fatal(err)
	}

	if got := f.Tools[toolName]; got.Repository != "sideways-works/pipelinegen" || got.Release.Version != "v0.9.1" || got.Release.SHA256 == "" {
		t.Fatalf("tool %+v", got)
	}
}

func TestTheConfigIsReadStrictly(t *testing.T) {
	for name, config := range map[string]map[string]any{
		"an unknown key":   {"resource": map[string]any{"remote": "x"}, "planner": map[string]any{"image": "g"}, "images": map[string]any{"g": map[string]any{"ref": "g", "digest": "sha256:" + strings.Repeat("0", 64)}}, "tags": []any{"x"}},
		"no remote":        {"planner": map[string]any{"image": "g"}, "images": map[string]any{"g": map[string]any{"ref": "g", "digest": "sha256:" + strings.Repeat("0", 64)}}},
		"a planner image":  {"resource": map[string]any{"remote": "x"}, "planner": map[string]any{"image": "h"}},
		"an image digest":  {"resource": map[string]any{"remote": "x"}, "planner": map[string]any{"image": "g"}, "images": map[string]any{"g": map[string]any{"ref": "g", "digest": "latest"}}},
		"a secret backend": {"resource": map[string]any{"remote": "x"}, "planner": map[string]any{"image": "g"}, "images": map[string]any{"g": map[string]any{"ref": "g", "digest": "sha256:" + strings.Repeat("0", 64)}}, "secrets": map[string]any{"from": "vault"}},
	} {
		if _, err := ReadSettings(config); !errors.Is(err, ErrSettings) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
