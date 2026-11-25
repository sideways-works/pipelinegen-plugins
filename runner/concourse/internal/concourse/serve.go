package concourse

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sideways-works/pipelinegen/plugin"

	"github.com/sideways-works/pipelinegen-plugins/runner/concourse/internal/runstep"
)

// Version is the plugin's version, set by the release build.
var Version = "dev"

// Serve answers pipelinegen's calls, and runs a step of the pipeline for
// `concourse-runner step`.
func Serve(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "step" {
		return runstep.Main(args[1:], stdout, stderr)
	}

	return plugin.Serve(args, stdin, stdout, stderr, map[string]plugin.Handler{
		plugin.CallDescribe: func([]byte) (any, error) { return describe(), nil },
		plugin.CallPipeline: pipeline,
	})
}

func pipeline(in []byte) (any, error) {
	var req plugin.PipelineRequest
	if err := json.Unmarshal(in, &req); err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}

	if err := olderKeys(req.Spec.CI, requestTags(in)); err != nil {
		return nil, err
	}

	olderForges(&req.Spec, in)

	pkgs, err := ReadPackages(in)
	if err != nil {
		return nil, err
	}

	p, err := GenerateAll(req.Spec, pkgs, req.Root, req.Config)
	if err != nil {
		return nil, err
	}

	text, err := Text(p)
	if err != nil {
		return nil, err
	}

	if err := os.WriteFile(filepath.Join(req.Work, "pipeline.yml"), text, 0o600); err != nil {
		return nil, fmt.Errorf("write pipeline: %w", err)
	}

	return plugin.PipelineAnswer{Files: []string{"pipeline.yml"}, Notices: Notices(req.Spec, req.Config)}, nil
}

func describe() plugin.Description {
	return plugin.Description{
		Protocol: plugin.Protocol, Kind: plugin.KindRunner, Name: "concourse-runner", Version: Version,
		Capabilities:  []string{plugin.CallPipeline, "repeat-at-run-time", "job-caches", "rerun", plugin.CapForgeChanges, capOlderChanges, plugin.CapSSHKeys, capContinuous, plugin.CapSteps, capPlacement, capPackages, plugin.CapStoreKeys},
		ResourceTypes: []string{mockType},
		// Concourse keeps a task's cache per job, step and worker.
		CacheScopes: map[string]string{
			"plan-cache": plugin.ScopeJob, "write-cache": plugin.ScopeJob, "cache": plugin.ScopeJob, packageCache: plugin.ScopeJob,
			// a suite's bookkeeping keeps the tools it fetched, each checked by its pin every build
			"tools": plugin.ScopeJob,
		},
	}
}
