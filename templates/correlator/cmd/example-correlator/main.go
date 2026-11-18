// Command example-correlator is a minimal correlator: a note's "see: <path>" line links its unit to
// the note it names, so a change to that note reaches every note that refers to it. Its units are
// the ones example-sbom (templates/sbom) lists.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"

	"github.com/sideways-works/pipelinegen/plugin"

	"github.com/sideways-works/pipelinegen-plugins/templates/correlator/note"
)

// Version is set to the release's tag when it is built.
var Version = "dev"

const roleReferences = "references"

func main() {
	os.Exit(plugin.Serve(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, map[string]plugin.Handler{
		plugin.CallDescribe: func([]byte) (any, error) { return describe(), nil },
		plugin.CallDetect:   detect,
		plugin.CallLinks:    links,
	}))
}

func describe() plugin.Description {
	return plugin.Description{
		Protocol:     plugin.Protocol,
		Kind:         plugin.KindCorrelator,
		Name:         "example-correlator",
		Version:      Version,
		Capabilities: []string{plugin.CallDetect, plugin.CallLinks},
		Candidates:   note.Candidates,
		Inputs:       map[string]string{plugin.CallDetect: plugin.PerFile, plugin.CallLinks: plugin.PerFile},
	}
}

// detect claims the notes that name another one.
func detect(in []byte) (any, error) {
	var req plugin.DetectRequest
	if err := json.Unmarshal(in, &req); err != nil {
		return nil, fmt.Errorf("reading the request: %w", err)
	}

	claims := []plugin.Claim{}

	for _, p := range req.Paths {
		data, ok, err := note.Read(req.Tree, p)
		if err != nil {
			return nil, err
		}

		if ok && len(note.See(data)) > 0 {
			claims = append(claims, plugin.Claim{Path: p, Role: roleReferences})
		}
	}

	return plugin.DetectAnswer{Claims: claims}, nil
}

// links answers, for each claimed note the request names or every claimed one, a link to each
// note it names.
func links(in []byte) (any, error) {
	var req plugin.TreeRequest
	if err := json.Unmarshal(in, &req); err != nil {
		return nil, fmt.Errorf("reading the request: %w", err)
	}

	var claimed []string

	for _, c := range req.Claimed {
		if c.Role == roleReferences {
			claimed = append(claimed, c.Path)
		}
	}

	asked := req.Paths
	if len(asked) == 0 {
		asked = claimed
	}

	ans := plugin.LinksAnswer{Links: []plugin.Link{}}

	for _, p := range asked {
		if !slices.Contains(claimed, p) {
			continue
		}

		data, ok, err := note.Read(req.Tree, p)
		if err != nil {
			return nil, err
		}

		if !ok {
			ans.CannotTell = append(ans.CannotTell, plugin.CannotTell{Unit: note.ID(p), Reason: "the file no longer starts with " + note.Header})

			continue
		}

		for _, to := range note.See(data) {
			ans.Links = append(ans.Links, plugin.Link{From: note.ID(p), To: note.ID(to), Kind: plugin.LinkUses})
		}

		ans.Inputs = append(ans.Inputs, p)
	}

	return ans, nil
}
