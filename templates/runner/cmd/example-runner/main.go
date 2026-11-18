// Command example-runner is a minimal runner: it writes a Concourse pipeline for the spec's branch
// and timer runs, with a planning job per run, a job per run and suite whose suites run the
// repository's task files, and a job that makes the pipeline again when the config changes.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sideways-works/pipelinegen/plugin"
	"go.yaml.in/yaml/v3"

	"github.com/sideways-works/pipelinegen-plugins/templates/runner/internal/pipeline"
)

// Version is set to the release's tag when it is built.
var Version = "dev"

var errRunner = errors.New("example-runner")

func main() {
	os.Exit(plugin.Serve(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, map[string]plugin.Handler{
		plugin.CallDescribe: func([]byte) (any, error) { return describe(), nil },
		plugin.CallPipeline: write,
	}))
}

func describe() plugin.Description {
	return plugin.Description{
		Protocol: plugin.Protocol, Kind: plugin.KindRunner, Name: "example-runner", Version: Version,
		// placement: every step runs on the workers the config's placement.default tags name
		Capabilities: []string{plugin.CapPlacement},
		// the caches each job's tasks keep stay with that job
		CacheScopes: map[string]string{"cache": plugin.ScopeJob, "plan-cache": plugin.ScopeJob},
	}
}

func write(in []byte) (any, error) {
	var req plugin.PipelineRequest
	if err := json.Unmarshal(in, &req); err != nil {
		return nil, fmt.Errorf("%w: request: %w", errRunner, err)
	}

	tags, err := pipeline.Placement(req.Config)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRunner, err)
	}

	p, notices := pipeline.Build(req.Spec, tags)

	text, err := yaml.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRunner, err)
	}

	if err := os.WriteFile(filepath.Join(req.Work, pipeline.File), text, 0o600); err != nil {
		return nil, fmt.Errorf("%w: %w", errRunner, err)
	}

	return plugin.PipelineAnswer{Files: []string{pipeline.File}, Notices: notices}, nil
}
