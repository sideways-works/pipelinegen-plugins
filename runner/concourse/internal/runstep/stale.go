package runstep

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"regexp"
	"strings"
)

const staleHash = "PIPELINEGEN_PIPELINE_SHA256"

// stale checks the pipeline set is the one the default branch's config
// writes. It never prints the pipeline or a diff, which would show its
// variables' values. A config the pinned tool can't read means a stale
// pipeline too.
func (t *Task) stale([]string) int {
	var errs bytes.Buffer

	cmd := t.command(t.Env, t.Dir, tool, "pipeline", "--root", "repo", "--plugins", "tools/plugins", "--gate", "newest", "--out", "pipeline.yml")
	cmd.Stderr = &errs

	if code := t.exited(tool, cmd.Run()); code != 0 {
		why := unreadable(errs.String())
		if why == "" {
			t.Stderr.Write(errs.Bytes()) //nolint:errcheck // the tool's own message, passed on

			return 1
		}

		t.sayStale()
		t.sayf("the pinned tool cannot read this tree's config: %s", why)

		return 1
	}

	// A build of a config the default branch no longer has checks nothing:
	// the newer config's build checks the pipeline.
	if !t.holds("newest/gate.json", `"1"`) {
		return 0
	}

	if !t.must(staleHash) {
		return 2
	}

	text, err := os.ReadFile(t.path("pipeline.yml"))
	if err != nil {
		t.sayf("%v", err)

		return 1
	}

	if pipelineHash(string(text)) != t.getenv(staleHash) {
		t.sayStale()

		return 1
	}

	t.outf("the pipeline is current")

	return 0
}

func (t *Task) sayStale() {
	t.sayf("the pipeline is stale: set it from the default branch with")
	t.sayf("  pipelinegen pipeline --out pipeline.yml && fly -t <target> set-pipeline -p %s -c pipeline.yml", t.getenv("PIPELINEGEN_PIPELINE"))
}

var hashLine = regexp.MustCompile(`^([ \t\v\f\r]*` + staleHash + `:).*$`)

// pipelineHash is the pipeline's checksum with its own hash param empty, as
// the runner sealed it.
func pipelineHash(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = hashLine.ReplaceAllString(line, `$1 ""`)
	}

	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))

	return hex.EncodeToString(sum[:])
}

const unreadableFrom = "pipelinegen pipeline: config "

// unreadable is what the tool said from its first line about the config on,
// on one line and at most 300 bytes; nothing when it said nothing about the
// config.
func unreadable(said string) string {
	lines := strings.Split(said, "\n")

	from := -1

	for i, l := range lines {
		if strings.HasPrefix(l, unreadableFrom) {
			from = i

			break
		}
	}

	if from < 0 {
		return ""
	}

	why := strings.Join(lines[from:], "\n")
	why = strings.NewReplacer("\n", " ", "\t", " ").Replace(why)

	for strings.Contains(why, "  ") {
		why = strings.ReplaceAll(why, "  ", " ")
	}

	why = strings.TrimRight(why, " ")
	if len(why) > 300 {
		why = why[:300]
	}

	return why
}
