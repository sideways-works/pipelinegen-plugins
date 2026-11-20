package concourse

import (
	"errors"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

// From pipelinegen v0.5.0 on, a plugin pinned by a zip is fetched and one pinned by a commit is
// refused; a pipelinegen before it keeps both as they were.
func TestZipAndCommitPinsFollowThePinnedPipelinegen(t *testing.T) {
	const url = "https://plugins.example.com/hello.zip"

	sum := strings.Repeat("e", 64)
	zipped := plugin.SpecTool{Name: "hello", Kind: "driver", Zip: &plugin.SpecZip{URL: url, SHA256: sum}}
	committed := plugin.SpecTool{Name: "go-sbom", Kind: "sbom", Repository: "o/go-sbom", Commit: strings.Repeat("c", 40)}
	runner := plugin.SpecTool{Name: "concourse", Kind: plugin.KindRunner, Repository: "o/plugins", Path: "runner/concourse", Version: "v1", SHA256: "abc"}
	zippedRunner := plugin.SpecTool{Name: "concourse", Kind: plugin.KindRunner, Zip: &plugin.SpecZip{URL: url, SHA256: sum}}

	release := func(v string) plugin.SpecTool {
		return plugin.SpecTool{Name: "pipelinegen", Kind: plugin.ToolPipelinegen, Repository: "o/pipelinegen", Version: v, SHA256: "aaa"}
	}
	commit := plugin.SpecTool{Name: "pipelinegen", Kind: plugin.ToolPipelinegen, Repository: "o/pipelinegen", Commit: strings.Repeat("0", 40)}

	for _, c := range []struct {
		name  string
		pg    plugin.SpecTool
		tools []plugin.SpecTool
		says  string
		lines string
	}{
		{name: "a zip from v0.5.0", pg: release("v0.5.0"), tools: []plugin.SpecTool{runner, zipped},
			lines: "concourse o/plugins v1 abc - runner/concourse\nhello - - - - - - " + url + " " + sum + "\n"},
		{name: "a zip before v0.5.0", pg: release("v0.4.2"), tools: []plugin.SpecTool{runner, zipped}, says: "which pipelinegen reads from v0.5.0 on"},
		{name: "a commit before v0.5.0", pg: release("v0.4.2"), tools: []plugin.SpecTool{runner, committed},
			lines: "concourse o/plugins v1 abc - runner/concourse\ngo-sbom o/go-sbom - - " + committed.Commit + "\n"},
		{name: "a commit from v0.5.0", pg: release("v0.5.1"), tools: []plugin.SpecTool{runner, committed},
			says: "build it into a zip (pipelinegen plugin build <dir> --zip <out.zip>)"},
		{name: "a runner by commit from v0.5.0", pg: release("v0.5.0"), tools: []plugin.SpecTool{{Name: "concourse", Kind: plugin.KindRunner, Repository: "o/plugins", Path: "runner/concourse", Commit: committed.Commit}},
			says: "plugins.concourse is pinned by commit"},
		{name: "a runner by zip", pg: release("v0.5.0"), tools: []plugin.SpecTool{zippedRunner}, says: "pin the runner by its release"},
		{name: "both with pipelinegen by commit", pg: commit, tools: []plugin.SpecTool{runner, zipped, committed},
			lines: "concourse o/plugins v1 abc - runner/concourse\nhello - - - - - - " + url + " " + sum + "\ngo-sbom o/go-sbom - - " + committed.Commit + "\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			spec, root := fixture(t)
			spec.Tools[0] = c.pg
			spec.Tools = append(spec.Tools, c.tools...)

			p, err := Generate(spec, root)
			if c.says != "" {
				if !errors.Is(err, ErrPin) || !strings.Contains(err.Error(), c.says) {
					t.Fatalf("got %v, want %q", err, c.says)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			seen := 0

			walkTasks(p, func(task M) {
				params := asMap(asMap(task["config"])["params"])
				if got, ok := params["PIPELINEGEN_PLUGINS"]; ok && got == c.lines {
					seen++
				}
			})

			if seen == 0 {
				t.Errorf("no task fetches the plugins as\n%s", c.lines)
			}
		})
	}
}
