package concourse

import (
	"encoding/json"
	"fmt"

	"github.com/sideways-works/pipelinegen/plugin"
)

type settings struct {
	URL        string         `json:"url"`
	Team       string         `json:"team"`
	Repository map[string]any `json:"repository"`
	Planner    struct {
		Image  map[string]any `json:"image"`
		Digest string         `json:"digest"`
		// Prepare fills the plugins' shared caches before planning.
		Prepare string `json:"prepare"`
		// Params and Secrets are the prepare command's alone.
		Params   map[string]string `json:"params"`
		Secrets  map[string]string `json:"secrets"`
		GitToken string            `json:"git_token"`
		// PrepareImage is a resource of the root file the plan task runs in.
		PrepareImage string `json:"prepare_image"`
	} `json:"planner"`
	Include      string         `json:"include"`
	PullRequests map[string]any `json:"pull_requests"`
	SelfSet      bool           `json:"self_set"`
	// SerialGroups says which groups are one per run.
	SerialGroups map[string]struct {
		PerRun bool `json:"per_run"`
	} `json:"serial_groups"`
}

func readSettings(ci map[string]any) (settings, error) {
	var s settings

	data, err := json.Marshal(ci)
	if err != nil {
		return s, fmt.Errorf("ci settings: %w", err)
	}

	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("ci settings: %w", err)
	}

	if s.Team == "" {
		s.Team = "main"
	}

	return s, nil
}

func tool(spec plugin.Spec, kind string) plugin.SpecTool {
	for _, t := range spec.Tools {
		if t.Kind == kind {
			return t
		}
	}

	return plugin.SpecTool{}
}
