package concourse

import (
	"encoding/json"

	"github.com/sideways-works/pipelinegen/plugin"
)

// olderKind and olderChanges are the names a pipelinegen before v0.6.0 gives the forge kind and its
// capability; capOlderChanges keeps such a pipelinegen planning a forge's runs with this runner.
const (
	olderKind       = "connector"
	capOlderChanges = "connector_changes"
)

// olderForges reads into spec what a pipelinegen before v0.6.0 wrote under the forge's older name: a
// run's trigger's connector, and a tool of kind connector. The spec's types know only forge.
func olderForges(spec *plugin.Spec, in []byte) {
	var raw struct {
		Spec struct {
			Runs []struct {
				Trigger struct {
					Connector string `json:"connector"`
				} `json:"trigger"`
			} `json:"runs"`
		} `json:"spec"`
	}

	if json.Unmarshal(in, &raw) != nil {
		return
	}

	for i, r := range raw.Spec.Runs {
		if i < len(spec.Runs) && spec.Runs[i].Trigger.Forge == "" {
			spec.Runs[i].Trigger.Forge = r.Trigger.Connector
		}
	}

	for i := range spec.Tools {
		if spec.Tools[i].Kind == olderKind {
			spec.Tools[i].Kind = plugin.KindForge
		}
	}
}
