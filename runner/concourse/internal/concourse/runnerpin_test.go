package concourse

import (
	"errors"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func TestTheRunnersPinIsOnGitHub(t *testing.T) {
	for repo, want := range map[string]string{
		"https://gitlab.com/g/plugins":      "fetches the runner from github.com, not https://gitlab.com/g/plugins",
		"git@git.example.com:g/plugins.git": "fetches the runner from github.com",
		"plugins":                           "neither owner/name nor a URL",
		"https://github.com/o/plugins":      "",
	} {
		spec, root := fixture(t)
		spec.Tools = append(spec.Tools, plugin.SpecTool{Name: "concourse", Kind: plugin.KindRunner, Repository: repo, Path: "runner/concourse", Version: "v1", SHA256: "abc"})

		_, err := Generate(spec, root)

		switch {
		case want == "" && err != nil:
			t.Errorf("%s: %v", repo, err)
		case want != "" && (!errors.Is(err, ErrRunnerPin) || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: got %v, want %q", repo, err, want)
		}
	}

	spec, root := fixture(t)
	spec.Tools = append(spec.Tools, plugin.SpecTool{Name: "concourse", Kind: plugin.KindRunner, Repository: "o/plugins", Path: "runner/../..", Version: "v1"})

	if _, err := Generate(spec, root); !errors.Is(err, ErrRunnerPin) || !strings.Contains(err.Error(), "leaves the repository") {
		t.Errorf("a path out of the repository: %v", err)
	}
}

// A pin by sums reaches the steps, and the runner's the bootstrap.
func TestPinsBySumsReachTheSteps(t *testing.T) {
	spec, root := fixture(t)
	spec.Tools[0] = plugin.SpecTool{Name: "pipelinegen", Kind: plugin.ToolPipelinegen, Repository: "o/pipelinegen", Version: "v1", Sums: "aaa"}
	spec.Tools = append(spec.Tools,
		plugin.SpecTool{Name: "concourse", Kind: plugin.KindRunner, Repository: "o/plugins", Path: "runner/concourse", Version: "v1", Sums: "bbb"},
		plugin.SpecTool{Name: "go-sbom", Kind: "sbom", Repository: "o/go-sbom", Version: "v2", Sums: "ccc"},
		plugin.SpecTool{Name: "ts-sbom", Kind: "sbom", Repository: "o/plugins", Path: "sbom/ts", Version: "v3", SHA256: "ddd"},
	)

	p, err := Generate(spec, root)
	if err != nil {
		t.Fatal(err)
	}

	seen := 0

	walkTasks(p, func(task M) {
		params := asMap(asMap(task["config"])["params"])
		if _, ok := params["PIPELINEGEN_PLUGINS"]; !ok {
			return
		}

		seen++

		for k, want := range map[string]string{
			"PIPELINEGEN_PLUGINS":   "concourse o/plugins v1 - - runner/concourse bbb\ngo-sbom o/go-sbom v2 - - - ccc\nts-sbom o/plugins v3 ddd - sbom/ts\n",
			"PIPELINEGEN_TOOL_SUMS": "aaa", "PIPELINEGEN_RUNNER_SUMS": "bbb", "PIPELINEGEN_RUNNER_SHA256": "",
		} {
			if got := params[k]; got != want {
				t.Errorf("%v: %s %q, want %q", task["task"], k, got, want)
			}
		}
	})

	if seen < 10 {
		t.Errorf("only %d tasks fetch the tools", seen)
	}
}
