package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/sideways-works/pipelinegen-sbom-npm/npm"
)

// Exit codes the protocol defines.
const (
	exitOK         = 0
	exitFailed     = 1
	exitCannotTell = 3
)

const (
	callDescribe = "describe"
	callUnits    = "units"
	callDocument = "document"
	callDetect   = "detect"
)

var (
	errUsage   = errors.New("usage: npm-sbom describe|units|document < request.json")
	errRequest = errors.New("request")
)

type Request struct {
	Protocol int        `json:"protocol"`
	Work     string     `json:"work"`
	Cache    string     `json:"cache"`
	Tree     string     `json:"tree"`
	Config   npm.Config `json:"config"`
	Paths    []string   `json:"paths"`
	Claimed  []struct {
		Path string `json:"path"`
		Role string `json:"role"`
	} `json:"claimed"`
	Narrowings []string `json:"narrowings"`
}

type description struct {
	Protocol     int               `json:"protocol"`
	Kind         string            `json:"kind"`
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Namespaces   []string          `json:"namespaces"`
	Capabilities []string          `json:"capabilities"`
	Candidates   []string          `json:"candidates"`
	Inputs       map[string]string `json:"inputs"`
	Narrowings   []any             `json:"narrowings"`
	Needs        []string          `json:"needs"`
	Caches       []string          `json:"caches"`
}

type unitSet struct {
	Group  string     `json:"group"`
	Inputs []string   `json:"inputs"`
	Units  []npm.Unit `json:"units"`
	Links  []npm.Link `json:"links"`
}

type unitsAnswer struct {
	Sets []unitSet `json:"sets"`
}

// group names the units answer's one set: a package installed by several
// lockfiles is one unit, so all their inputs form one set.
const group = "npm"

var Version = "devel"

func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		say(stderr, errUsage)

		return exitFailed
	}

	answer, err := call(args[0], stdin)
	if err != nil {
		say(stderr, "npm-sbom:", err)

		if errors.Is(err, npm.ErrCannotTell) {
			return exitCannotTell
		}

		return exitFailed
	}

	if err := json.NewEncoder(stdout).Encode(answer); err != nil {
		say(stderr, "npm-sbom:", err)

		return exitFailed
	}

	return exitOK
}

// say writes a line for people; a failed write has nobody left to tell.
func say(w io.Writer, a ...any) {
	if _, err := fmt.Fprintln(w, a...); err != nil {
		return
	}
}

func call(name string, stdin io.Reader) (any, error) {
	if name == callDescribe {
		return describe(), nil
	}

	var req Request
	if err := json.NewDecoder(stdin).Decode(&req); err != nil {
		return nil, fmt.Errorf("%w: %w", errRequest, err)
	}

	if req.Protocol != 1 {
		return nil, fmt.Errorf("%w: protocol %d, this plugin speaks 1", errRequest, req.Protocol)
	}

	if len(req.Config.Lockfiles) == 0 && name != callDetect {
		var found []npm.Detection

		for _, c := range req.Claimed {
			found = append(found, npm.Detection{Path: c.Path, Role: c.Role})
		}

		if len(found) == 0 && req.Paths != nil {
			found = npm.Detect(req.Tree, req.Paths)
		}

		req.Config.Lockfiles = npm.Lockfiles(found)
	}

	// Only a run by hand, with neither claims nor paths, walks the tree for
	// lockfiles.
	build := npm.BuildFiles
	if len(req.Claimed) == 0 && req.Paths == nil {
		build = npm.Build
	}

	switch name {
	case callDetect:
		return map[string][]npm.Detection{"claims": nonNil(npm.Detect(req.Tree, req.Paths))}, nil
	case callUnits:
		m, err := build(req.Tree, req.Config)
		if err != nil {
			return nil, fmt.Errorf("units: %w", err)
		}

		return units(req, m), nil
	case callDocument:
		m, err := build(req.Tree, req.Config)
		if err != nil {
			return nil, fmt.Errorf("document: %w", err)
		}

		return m.Document("npm-sbom", Version), nil
	default:
		return nil, fmt.Errorf("%w: unknown call %q", errUsage, name)
	}
}

func units(req Request, m *npm.Model) unitsAnswer {
	holds := func(p string) bool {
		if slices.Contains(m.Inputs, p) {
			return true
		}

		_, err := os.Stat(filepath.Join(req.Tree, filepath.FromSlash(p)))

		return errors.Is(err, os.ErrNotExist)
	}

	if req.Paths != nil && !slices.ContainsFunc(req.Paths, holds) {
		return unitsAnswer{Sets: []unitSet{}}
	}

	return unitsAnswer{Sets: []unitSet{{
		Group: group, Inputs: nonNil(m.Inputs), Units: nonNil(m.Units), Links: nonNil(m.Links),
	}}}
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}

	return s
}

func describe() description {
	return description{
		Protocol:     1,
		Kind:         "sbom",
		Name:         "npm-sbom",
		Version:      Version,
		Namespaces:   []string{"dep:npm"},
		Capabilities: []string{callDetect, callUnits, callDocument},
		Candidates:   npm.CandidatePatterns,
		// A lockfile's units depend on the lockfile, the manifest beside it
		// and the declared Node version together.
		Inputs:     map[string]string{callDetect: "per-file", callUnits: "per-set", callDocument: "per-set"},
		Narrowings: []any{},
		Needs:      []string{},
		Caches:     []string{},
	}
}
