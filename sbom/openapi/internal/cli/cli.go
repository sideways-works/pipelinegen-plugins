package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen-sbom-openapi/openapi"
)

// Exit codes the protocol defines.
const (
	exitOK         = 0
	exitFailed     = 1
	exitCannotTell = 3
)

type Config struct {
	Documents []openapi.Document `json:"documents"`
	// KeepDocs keeps documentation in the digests, for tests that assert on it.
	KeepDocs bool `json:"keep_docs,omitempty"`
}

type Request struct {
	Protocol int      `json:"protocol"`
	Work     string   `json:"work"`
	Cache    string   `json:"cache"`
	Tree     string   `json:"tree"`
	Config   Config   `json:"config"`
	Paths    []string `json:"paths"`
	Claimed  []struct {
		Path string `json:"path"`
		Role string `json:"role"`
	} `json:"claimed"`
	Narrowings []string `json:"narrowings"`
}

type narrowing struct {
	Name  string `json:"name"`
	OffAt int    `json:"off_at"`
	Note  string `json:"note"`
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
	Narrowings   []narrowing       `json:"narrowings"`
	Needs        []string          `json:"needs"`
	Caches       []string          `json:"caches"`
}

type unitSet struct {
	Group  string         `json:"group"`
	Inputs []string       `json:"inputs"`
	Units  []openapi.Unit `json:"units"`
	Links  []openapi.Link `json:"links"`
}

type unitsAnswer struct {
	Sets []unitSet `json:"sets"`
}

// contractsGroup is the one set of every contract: each operation uses every
// document's routing table.
const contractsGroup = "contracts"

var (
	errUsage   = errors.New("usage: openapi-sbom describe|units|document < request.json")
	errRequest = errors.New("request")
)

const (
	callDescribe = "describe"
	callUnits    = "units"
	callDocument = "document"
	callDetect   = "detect"
)

// How a call's answer depends on its files: per-file on that file alone,
// per-set on the files a set of the answer names.
const (
	perFile = "per-file"
	perSet  = "per-set"
)

var Version = "devel"

func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		say(stderr, errUsage)

		return exitFailed
	}

	answer, err := call(args[0], stdin)
	if err != nil {
		say(stderr, "openapi-sbom:", err)

		if errors.Is(err, openapi.ErrCannotTell) {
			return exitCannotTell
		}

		return exitFailed
	}

	enc := json.NewEncoder(stdout)
	if err := enc.Encode(answer); err != nil {
		say(stderr, "openapi-sbom:", err)

		return exitFailed
	}

	return exitOK
}

func call(name string, stdin io.Reader) (any, error) {
	if name == callDescribe {
		return describe(), nil
	}

	var req Request
	if err := json.NewDecoder(stdin).Decode(&req); err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}

	if req.Protocol != 1 {
		return nil, fmt.Errorf("%w: protocol %d, this plugin speaks 1", errRequest, req.Protocol)
	}

	switch name {
	case callDetect:
		return map[string][]openapi.Detection{"claims": nonNil(openapi.Detect(req.Tree, req.Paths))}, nil
	case callUnits:
		m, err := build(req, false)
		if err != nil {
			return nil, err
		}

		return unitsAnswer{Sets: sets(m, req.Paths)}, nil
	case callDocument:
		m, err := build(req, true)
		if err != nil {
			return nil, err
		}

		return cycloneDX(m, Version, asked(req.Paths)), nil
	default:
		return nil, fmt.Errorf("%w: unknown call %q", errUsage, name)
	}
}

func asked(paths []string) map[string]bool {
	if paths == nil {
		return nil
	}

	out := make(map[string]bool, len(paths))
	for _, p := range paths {
		out[path.Clean(p)] = true
	}

	return out
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
		Name:         "openapi-sbom",
		Version:      Version,
		Namespaces:   []string{openapi.Namespace},
		Capabilities: []string{callDetect, callUnits, callDocument},
		// a document's bill of materials depends on the files its paths refer to
		Inputs:     map[string]string{callDetect: perFile, callUnits: perSet, callDocument: perSet},
		Candidates: openapi.CandidatePatterns,
		Narrowings: []narrowing{{
			Name:  openapi.NarrowDocText,
			OffAt: 5,
			Note:  "descriptions, summaries, titles and examples change no digest; a test that reads them from the served document can be missed",
		}, {
			Name:  openapi.NarrowDocument,
			OffAt: 2,
			Note:  "a test that depends on a contract file is bound to the operations of it the test touched; a test that reads the file whole is bound to the whole file only where a correlator says so",
		}},
		Needs:  []string{},
		Caches: []string{},
	}
}

// build reads the documents the request asks about, detecting them itself
// when pipelinegen sends neither claims nor paths. Units come from every
// document, since each operation uses every routing table; only the document
// call keeps to paths.
func build(req Request, onlyPaths bool) (*openapi.Model, error) {
	docs, err := documents(req)
	if err != nil {
		return nil, err
	}

	if onlyPaths && req.Paths != nil {
		docs = slices.DeleteFunc(slices.Clone(docs), func(d openapi.Document) bool {
			return !slices.Contains(req.Paths, path.Clean(d.Path))
		})
	}

	// The answer is the same at every narrowing level: documentation sits
	// in units of its own, linked while doc-text is off.
	keep := req.Config.KeepDocs

	m, err := openapi.Build(req.Tree, docs, openapi.Options{KeepDocs: keep})
	if err != nil {
		return nil, fmt.Errorf("units: %w", err)
	}

	return m, nil
}

// sets splits the units into one set of every contract and one set per file
// of parts. A path no set holds (a deleted contract) answers the contracts'
// set.
func sets(m *openapi.Model, paths []string) []unitSet {
	contracts := m.Contracts()
	fileOf := map[string]string{}
	byFile := map[string]*unitSet{}

	var order []string

	setOf := func(file string) *unitSet {
		if slices.Contains(contracts, file) {
			file = ""
		}

		if s, ok := byFile[file]; ok {
			return s
		}

		s := &unitSet{Group: file, Inputs: inputs(m, file), Units: []openapi.Unit{}, Links: []openapi.Link{}}
		if file == "" {
			s.Group, s.Inputs = contractsGroup, inputs(m, contracts...)
		}

		byFile[file] = s
		order = append(order, file)

		return s
	}

	if len(contracts) > 0 {
		setOf(contracts[0])
	}

	for _, u := range m.Units {
		fileOf[u.ID] = u.File
		s := setOf(u.File)
		s.Units = append(s.Units, u)
	}

	for _, l := range m.Links {
		f, ok := fileOf[l.From]
		if !ok {
			f = strings.TrimPrefix(l.From, "file:")
		}

		s := setOf(f)
		s.Links = append(s.Links, l)
	}

	out := []unitSet{}

	for _, f := range order {
		s := byFile[f]
		if paths == nil || slices.ContainsFunc(paths, func(p string) bool { return holds(s, byFile, path.Clean(p)) }) {
			out = append(out, *s)
		}
	}

	return out
}

// inputs are the files a set's units were read from, read-along files
// included; the core caches the set by all of them.
func inputs(m *openapi.Model, files ...string) []string {
	out := make([]string, 0, len(files))

	for _, f := range files {
		out = append(out, f)
		out = append(out, m.ReadFiles(f)...)
	}

	slices.Sort(out)

	return slices.Compact(out)
}

// holds tells whether a set answers for a path: one of its inputs, or for the
// contracts' set any path that is no file of parts.
func holds(s *unitSet, byFile map[string]*unitSet, p string) bool {
	if slices.Contains(s.Inputs, p) {
		return true
	}

	if s.Group != contractsGroup {
		return false
	}

	_, fragment := byFile[p]

	return !fragment
}

// say writes a line for people; a failed write has nobody left to tell.
func say(w io.Writer, a ...any) {
	if _, err := fmt.Fprintln(w, a...); err != nil {
		return
	}
}

func documents(req Request) ([]openapi.Document, error) {
	var docs []openapi.Document

	add := func(p string) {
		p = path.Clean(p)
		if !slices.ContainsFunc(docs, func(d openapi.Document) bool { return d.Path == p }) {
			docs = append(docs, openapi.Document{Path: p})
		}
	}

	for _, c := range req.Claimed {
		add(c.Path)
	}

	// A path asked about that is gone from the tree (a deleted contract)
	// has nothing to read; its set is answered from the rest.
	for _, p := range req.Paths {
		if _, err := os.Stat(filepath.Join(req.Tree, filepath.FromSlash(p))); err == nil {
			add(p)
		}
	}

	if req.Paths == nil && len(req.Claimed) == 0 && len(req.Config.Documents) == 0 {
		candidates, err := openapi.Candidates(req.Tree)
		if err != nil {
			return nil, fmt.Errorf("detect: %w", err)
		}

		docs = openapi.Documents(openapi.Detect(req.Tree, candidates))
	}

	for _, c := range req.Config.Documents {
		i := slices.IndexFunc(docs, func(d openapi.Document) bool { return d.Path == path.Clean(c.Path) })
		if i >= 0 {
			docs[i] = c

			continue
		}

		docs = append(docs, c)
	}

	return docs, nil
}
