// Package cli answers pipelinegen's correlator calls: describe, links,
// observe, collect and why.
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

	"github.com/sideways-works/pipelinegen-correlator-oapi-codegen/correlate"
	"github.com/sideways-works/pipelinegen-sbom-openapi/openapi"
)

// Exit codes the protocol defines.
const (
	exitOK         = 0
	exitFailed     = 1
	exitCannotTell = 3
)

// The calls this plugin answers.
const (
	callDescribe = "describe"
	callLinks    = "links"
	callObserve  = "observe"
	callCollect  = "collect"
	callWhy      = "why"
	callDetect   = "detect"
)

var (
	errUsage   = errors.New("usage: oapi-codegen-correlator describe|links|observe|collect|why < request.json")
	errRequest = errors.New("request")
)

// Request is what every call reads on stdin; each call uses its own part.
type Request struct {
	Protocol   int              `json:"protocol"`
	Work       string           `json:"work"`
	Cache      string           `json:"cache"`
	Tree       string           `json:"tree"`
	Config     correlate.Config `json:"config"`
	Narrowings []string         `json:"narrowings"`
	Slice      string           `json:"slice"`
	Dirs       []string         `json:"dirs"`
	Action     string           `json:"action"`
	Dir        string           `json:"dir"`
	Paths      []string         `json:"paths"`
	Claimed    []claim          `json:"claimed"`
	From       string           `json:"from"`
	To         string           `json:"to"`
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

type cannotTell struct {
	Unit   string `json:"unit"`
	Reason string `json:"reason"`
}

// A linkSet is the links answer's one set: which code reaches the document
// depends on every Go file together. The digest relations travel in it; at
// the answer's top level they would make the core ask about every claim on
// every plan.
type linkSet struct {
	Group      string               `json:"group"`
	Inputs     []string             `json:"inputs"`
	Links      []correlate.Link     `json:"links"`
	DigestFrom []correlate.Relation `json:"digest_from"`
	CannotTell []cannotTell         `json:"cannot_tell"`
}

type linksAnswer struct {
	Sets []linkSet `json:"sets"`
}

const group = "oapi-codegen"

type step struct {
	Unit string `json:"unit"`
	Note string `json:"note"`
}

// Version is what describe reports.
var Version = "devel"

// Run answers one call and returns the exit code.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		say(stderr, errUsage)

		return exitFailed
	}

	answer, err := call(args[0], stdin)
	if err != nil {
		say(stderr, "oapi-codegen-correlator:", err)

		if errors.Is(err, openapi.ErrCannotTell) || errors.Is(err, correlate.ErrNoData) || errors.Is(err, correlate.ErrNoContract) ||
			errors.Is(err, correlate.ErrUnknownRoute) {
			return exitCannotTell
		}

		return exitFailed
	}

	if err := json.NewEncoder(stdout).Encode(answer); err != nil {
		say(stderr, "oapi-codegen-correlator:", err)

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

	switch name {
	case callDetect:
		return map[string][]correlate.Detection{"claims": nonNil(correlate.Detect(req.Tree, req.Paths, req.Config))}, nil
	case callCollect:
		if err := correlate.Collect(req.Config.RequestLog, req.Action, req.Dir); err != nil {
			return nil, fmt.Errorf("collect: %w", err)
		}

		return struct{}{}, nil
	case callLinks:
		return links(req)
	case callObserve:
		return observe(req)
	case callWhy:
		return why(req)
	default:
		return nil, fmt.Errorf("%w: unknown call %q", errUsage, name)
	}
}

func describe() description {
	return description{
		Protocol:     1,
		Kind:         "correlator",
		Name:         "oapi-codegen-correlator",
		Version:      Version,
		Namespaces:   []string{},
		Capabilities: []string{callDetect, callLinks, callObserve, callCollect, callWhy},
		Candidates:   correlate.CandidatePatterns,
		Inputs:       map[string]string{callDetect: "per-file", callLinks: "per-set"},
		// document-units is openapi-sbom's narrowing; the embedded
		// document's links carry it too.
		Narrowings: []narrowing{{
			Name:  correlate.NarrowGenerated,
			OffAt: 5,
			Note:  "code generated from the whole contract takes its digest from the contract, the generator's version and its config; a hand edit to generated code is missed",
		}},
		Needs:  []string{},
		Caches: []string{},
	}
}

func links(req Request) (linksAnswer, error) {
	m, err := correlate.Load(req.Tree, req.Config, req.files())
	if err != nil {
		return linksAnswer{}, fmt.Errorf("load: %w", err)
	}

	all, err := allLinks(m, req)
	if err != nil {
		return linksAnswer{}, err
	}

	refs, err := m.Referrers(req.Tree)
	if err != nil {
		return linksAnswer{}, fmt.Errorf("scan: %w", err)
	}

	set := linkSet{
		Group: group, Inputs: nonNil(m.Inputs), Links: nonNil(all),
		DigestFrom: nonNil(m.DigestFrom()), CannotTell: []cannotTell{},
	}

	// A generated package whose contract the tree doesn't name embeds a
	// document nobody can relate to its operations; it is bound wide.
	for _, u := range m.Unknown {
		set.CannotTell = append(set.CannotTell, cannotTell{
			Unit:   correlate.GoUnit(path.Dir(u.Path), "var", "swaggerSpec"),
			Reason: u.Path + " was generated by oapi-codegen, but no go:generate line beside it names the contract; add the document to the config",
		})
	}

	// A referrer nobody classified may read any part of the document; the
	// core binds it wide until the config says which kind it is.
	for _, r := range refs {
		if r.As == "" {
			set.CannotTell = append(set.CannotTell, cannotTell{
				Unit:   r.ID,
				Reason: "reaches the document " + r.Document + " through " + r.Through + "; list it under readers or per_operation",
			})
		}
	}

	ans := linksAnswer{Sets: []linkSet{}}
	if holdsAny(req, m.Inputs) {
		ans.Sets = append(ans.Sets, set)
	}

	return ans, nil
}

func holdsAny(req Request, inputs []string) bool {
	if req.Paths == nil {
		return true
	}

	return slices.ContainsFunc(req.Paths, func(p string) bool {
		if slices.Contains(inputs, p) {
			return true
		}

		_, err := os.Stat(filepath.Join(req.Tree, filepath.FromSlash(p)))

		return errors.Is(err, os.ErrNotExist)
	})
}

func allLinks(m *correlate.Model, req Request) ([]correlate.Link, error) {
	hl, err := m.HandlerLinks(req.Tree)
	if err != nil {
		return nil, fmt.Errorf("handlers: %w", err)
	}

	mr, err := m.MapReaders(req.Tree)
	if err != nil {
		return nil, fmt.Errorf("map readers: %w", err)
	}

	return append(append(m.CodeLinks(), hl...), mr...), nil
}

func observe(req Request) (map[string][]string, error) {
	files, err := req.observed()
	if err != nil {
		return nil, err
	}

	m, err := correlate.Load(req.Tree, req.Config, files)
	if err != nil {
		return nil, fmt.Errorf("load: %w", err)
	}

	touched, err := m.Observe(req.Dirs)
	if err != nil {
		return nil, fmt.Errorf("observe: %w", err)
	}

	return map[string][]string{"touched": touched}, nil
}

// why explains a link this plugin writes and, for the embedded document,
// lists what the accessor scan found and how each is classified.
func why(req Request) (map[string][]step, error) {
	m, err := correlate.Load(req.Tree, req.Config, req.files())
	if err != nil {
		return nil, fmt.Errorf("load: %w", err)
	}

	all, err := allLinks(m, req)
	if err != nil {
		return nil, err
	}

	var path []step

	for _, l := range all {
		if l.From == req.From && l.To == req.To {
			path = []step{{Unit: l.From, Note: l.Note}, {Unit: l.To, Note: ""}}

			break
		}
	}

	if len(path) > 0 && strings.Contains(req.From, "#var:") {
		refs, err := m.Referrers(req.Tree)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}

		for _, r := range refs {
			as := r.As
			if as == "" {
				as = "not classified, bound wide"
			}

			path = append(path, step{Unit: r.ID, Note: "reaches " + r.Document + " through " + r.Through + ": " + as})
		}
	}

	if path == nil {
		path = []step{}
	}

	return map[string][]step{"path": path}, nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}

	return s
}

type claim struct {
	Path string `json:"path"`
	Role string `json:"role"`
}

// observed are the files observe reads: the claimed contracts, else those
// among the tree's candidates. Go code is never read.
func (r Request) observed() ([]string, error) {
	if len(r.Claimed) > 0 {
		var out []string

		for _, c := range r.Claimed {
			if c.Role == correlate.RoleContract {
				out = append(out, c.Path)
			}
		}

		return nonNil(out), nil
	}

	found, err := openapi.Candidates(r.Tree)
	if err != nil {
		return nil, fmt.Errorf("observe: %w", err)
	}

	return nonNil(found), nil
}

// files are the files to read generated code and directives from: the
// files detection claimed, else the paths asked about, else (nil) the
// whole tree.
func (r Request) files() []string {
	if len(r.Claimed) == 0 {
		return r.Paths
	}

	out := make([]string, 0, len(r.Claimed))
	for _, c := range r.Claimed {
		out = append(out, c.Path)
	}

	return out
}
