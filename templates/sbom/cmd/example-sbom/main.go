// Command example-sbom is a minimal sbom plugin: every note (a *.note file that starts with
// "# note") is a unit, note:<path>, whose digest changes when its text does.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"

	"github.com/sideways-works/pipelinegen/plugin"

	"github.com/sideways-works/pipelinegen-plugins/templates/sbom/note"
)

// Version is set to the release's tag when it is built.
var Version = "dev"

const roleNote = "note"

func main() {
	os.Exit(plugin.Serve(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, map[string]plugin.Handler{
		plugin.CallDescribe: func([]byte) (any, error) { return describe(), nil },
		plugin.CallDetect:   detect,
		plugin.CallUnits:    units,
	}))
}

func describe() plugin.Description {
	return plugin.Description{
		Protocol:     plugin.Protocol,
		Kind:         plugin.KindSBOM,
		Name:         "example-sbom",
		Version:      Version,
		Namespaces:   []string{note.Namespace},
		Capabilities: []string{plugin.CallDetect, plugin.CallUnits},
		Candidates:   note.Candidates,
		// each answer depends on one file alone, so the core caches it by that file's content
		Inputs: map[string]string{plugin.CallDetect: plugin.PerFile, plugin.CallUnits: plugin.PerFile},
	}
}

// detect claims the candidates that are notes, by their first line.
func detect(in []byte) (any, error) {
	var req plugin.DetectRequest
	if err := json.Unmarshal(in, &req); err != nil {
		return nil, fmt.Errorf("reading the request: %w", err)
	}

	claims := []plugin.Claim{}

	for _, p := range req.Paths {
		_, ok, err := note.Read(req.Tree, p)
		if err != nil {
			return nil, err
		}

		if ok {
			claims = append(claims, plugin.Claim{Path: p, Role: roleNote})
		}
	}

	return plugin.DetectAnswer{Claims: claims}, nil
}

// units lists a unit per claimed note: those the request names, or every claimed one.
func units(in []byte) (any, error) {
	var req plugin.TreeRequest
	if err := json.Unmarshal(in, &req); err != nil {
		return nil, fmt.Errorf("reading the request: %w", err)
	}

	var claimed []string

	for _, c := range req.Claimed {
		if c.Role == roleNote {
			claimed = append(claimed, c.Path)
		}
	}

	asked := req.Paths
	if len(asked) == 0 {
		asked = claimed
	}

	ans := plugin.UnitsAnswer{Units: []plugin.Unit{}}

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

		ans.Units = append(ans.Units, plugin.Unit{ID: note.ID(p), Digest: note.Digest(data), File: p, Kind: roleNote, Label: p})
		ans.Inputs = append(ans.Inputs, p)
	}

	return ans, nil
}
