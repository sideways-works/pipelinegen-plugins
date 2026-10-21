package concourse

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var leaks = map[string]*regexp.Regexp{
	"shell tracing":                     regexp.MustCompile(`(?m)\bset\s+-[a-wyz]*x|\bbash\s+-x\b|\bsh\s+-x\b`),
	"verbose or tracing curl":           regexp.MustCompile(`curl[^\n]*\s(-v|--verbose|--trace|--trace-ascii)\b`),
	"credentials forwarded on redirect": regexp.MustCompile(`--location-trusted`),
	"environment dumps":                 regexp.MustCompile(`(?m)\b(printenv|env)\s*$|\bexport\s+-p\b|\bset\s*$`),
	"credentials in a URL":              regexp.MustCompile(`://[^/\s"'@]+:[^/\s"'@]+@`),
	"netrc":                             regexp.MustCompile(`\.netrc`),
	"git tracing":                       regexp.MustCompile(`GIT_TRACE|GIT_CURL_VERBOSE`),
}

func TestNothingPrintsASecret(t *testing.T) {
	for _, selfSet := range []bool{false, true} {
		spec, root := fixture(t)
		spec.CI["self_set"] = selfSet

		p, err := Generate(spec, root)
		if err != nil {
			t.Fatal(err)
		}

		sources := map[string]string{
			"the pipeline": text(t, p), "bootstrap.sh": bootstrapScript,
			"fallback.sh": fallbackScript, "stale.sh": staleScript, "plan.sh": planScript,
			"planlib.sh": planLib, "sweep.sh": sweepScript, "sweep-plan.sh": sweepPlan, "sweep-write.sh": sweepWrite,
			"proposed-plan.sh": proposedPlan, "proposed-write.sh": proposedWrite,
		}

		for name, src := range sources {
			for what, re := range leaks {
				if m := re.FindString(src); m != "" {
					t.Errorf("%s (self_set %v): %s: %q", name, selfSet, what, m)
				}
			}
		}

		// set_pipeline vars would be stored interpolated and shown in its diff.
		if strings.Contains(text(t, p), "set_pipeline") && regexp.MustCompile(`(?s)set_pipeline: self[^-]*vars:`).MatchString(text(t, p)) {
			t.Errorf("self_set %v: set_pipeline passes variables", selfSet)
		}
	}
}

func TestPipelineJobSetsOnlyWhenAsked(t *testing.T) {
	spec, root := fixture(t)

	p, err := Generate(spec, root)
	if err != nil {
		t.Fatal(err)
	}

	s := text(t, p)
	if strings.Contains(s, "set_pipeline") || !strings.Contains(s, "task: stale") {
		t.Fatal("without self_set the pipeline job must only check")
	}

	spec.CI["self_set"] = true

	if p, err = Generate(spec, root); err != nil {
		t.Fatal(err)
	}

	if s := text(t, p); !strings.Contains(s, "set_pipeline: self") || strings.Contains(s, "task: stale") {
		t.Fatal("with self_set the pipeline job sets the pipeline")
	}
}

func TestStaleCheck(t *testing.T) {
	spec, root := fixture(t)

	p, err := Generate(spec, root)
	if err != nil {
		t.Fatal(err)
	}

	current := text(t, p)

	norm := regexp.MustCompile(`(?m)^(\s*`+staleHash+`:).*$`).ReplaceAllString(current, `$1 ""`)
	sum := sha256.Sum256([]byte(norm))
	want := hex.EncodeToString(sum[:])

	if !strings.Contains(current, want) {
		t.Fatalf("the pipeline doesn't carry its hash %s", want)
	}

	for _, c := range []struct {
		name, written, fails string
		ok                   bool
		says                 []string
	}{
		{name: "current", written: current, ok: true, says: []string{"the pipeline is current"}},
		{name: "stale", written: current + "# changed\n", says: []string{"the pipeline is stale"}},
		// A build of an older config, beside a newer one's, checks nothing.
		{name: "older", ok: true},
		// A pinned tool older than the tree's config can't read it.
		{
			name: "unreadable", fails: "pipelinegen pipeline: config /b/repo/pipelinegen.yaml: parse config: yaml: unmarshal errors:\n  line 45: field secrets not found in type config.Suite",
			says: []string{"the pipeline is stale", "set-pipeline", "the pinned tool cannot read this tree's config: pipelinegen pipeline: config /b/repo/pipelinegen.yaml: parse config: yaml: unmarshal errors: line 45: field secrets not found in type config.Suite\n"},
		},
		// Anything else is a failure of its own, not a stale pipeline.
		{name: "broken", fails: "pipelinegen pipeline: runner: exit status 2", says: []string{"pipelinegen pipeline: runner: exit status 2"}},
	} {
		work := t.TempDir()
		fake := filepath.Join(work, "tools", "pipelinegen")

		if err := os.MkdirAll(filepath.Dir(fake), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(work, "want.yml"), []byte(c.written), 0o600); err != nil {
			t.Fatal(err)
		}

		script := "#!/bin/sh\nmkdir -p newest && echo '[\"1\"]' > newest/gate.json\ncp want.yml pipeline.yml\n"

		switch {
		case c.fails != "":
			script = "#!/bin/sh\ncat >&2 <<'EOF'\n" + c.fails + "\nEOF\nexit 1\n"
		case c.name == "older":
			script = "#!/bin/sh\nmkdir -p newest && echo '[]' > newest/gate.json\necho superseded by 0123456789ab\n"
		}

		if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}

		cmd := exec.CommandContext(t.Context(), "sh", "-ec", staleScript)
		cmd.Dir = work

		cmd.Env = append(os.Environ(), staleHash+"="+want, "PIPELINEGEN_PIPELINE=fixture", "GITHUB_TOKEN=secret-value")

		out, err := cmd.CombinedOutput()
		if (err == nil) != c.ok {
			t.Errorf("%s: %v\n%s", c.name, err, out)
		}

		if strings.Contains(string(out), "secret-value") || strings.Contains(string(out), "jobs:") {
			t.Errorf("%s: the check printed more than the fact: %s", c.name, out)
		}

		for _, want := range c.says {
			if !strings.Contains(string(out), want) {
				t.Errorf("%s: the check doesn't say %q: %s", c.name, want, out)
			}
		}

		if stale := strings.Contains(string(out), "is stale"); stale != (c.name == "stale" || c.name == "unreadable") {
			t.Errorf("%s: stale %v: %s", c.name, stale, out)
		}

		if c.name == "stale" && strings.Contains(string(out), "cannot read") {
			t.Errorf("a differing checksum blames the config: %s", out)
		}
	}
}
