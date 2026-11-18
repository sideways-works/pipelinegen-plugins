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
