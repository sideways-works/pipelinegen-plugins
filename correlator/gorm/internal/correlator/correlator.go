// Package correlator answers pipelinegen's plugin calls (describe, detect,
// links, why) from the analysis of a tree.
package correlator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/plugin"

	"github.com/sideways-works/pipelinegen-correlator-gorm/internal/analysis"
	"github.com/sideways-works/pipelinegen-correlator-gorm/internal/sqlrefs"
)

// Name is the plugin's name.
const Name = "gorm-correlator"

// CallInspect is not part of the protocol: it prints what the analysis of
// a tree found, for people tuning the config.
const CallInspect = "inspect"

// The roles of claimed files, in the protocol's words.
const (
	RoleManifest = "manifest"
	RoleSource   = "source"
	// RoleModuleFile is a go.mod of a module that doesn't require GORM, a
	// go.sum or a go.work: read to load a module, never a module to answer.
	RoleModuleFile = "module-file"
)

// inlineLinks is how many links an answer carries on stdout; more go into
// a JSON Lines file of sets in the work directory.
var inlineLinks = 5000

// Narrowings are the narrowings this correlator implements; the wider rule's
// links go out tagged while:off.
var Narrowings = []plugin.Narrowing{
	{Name: analysis.NarrowModels, OffAt: 5, Note: "a statement with Table() binds that table only, and a model's hooks only for the statements that call them"},
	{Name: analysis.NarrowAssociations, OffAt: 5, Note: "saving a literal that sets no association binds none of the associations' tables"},
	{Name: analysis.NarrowModelTypes, OffAt: 5, Note: "a model's type is not linked to its table; call sites link on their own"},
}

// Describe answers describe.
func Describe(version string) plugin.Description {
	return plugin.Description{
		Protocol: plugin.Protocol, Kind: plugin.KindCorrelator, Name: Name, Version: version,
		Capabilities: []string{plugin.CallDetect, plugin.CallLinks, plugin.CallWhy},
		Narrowings:   Narrowings,
		Needs:        []string{"go"},
		Caches:       []string{"gocache", "gomodcache"},
		Candidates:   analysis.Candidates,
		Inputs:       map[string]string{plugin.CallDetect: plugin.PerFile, plugin.CallLinks: plugin.PerSet},
	}
}

// WhyRequest asks how one unit reaches another.
type WhyRequest struct {
	plugin.TreeRequest

	From string `json:"from"`
	To   string `json:"to"`
}

// Step is one step of a why answer.
type Step struct {
	Unit string `json:"unit"`
	Note string `json:"note,omitempty"`
}

// WhyAnswer is the path from one unit to another.
type WhyAnswer struct {
	Path []Step `json:"path"`
}

// ErrRequest wraps a request the plugin can't read.
var ErrRequest = errors.New("reading the request")

func decode(in []byte, req any) error {
	if err := json.Unmarshal(in, req); err != nil {
		return fmt.Errorf("%w: %w", ErrRequest, err)
	}

	return nil
}

func config(req plugin.TreeRequest) (analysis.Config, error) {
	var cfg analysis.Config

	if req.Config == nil {
		return cfg, nil
	}

	b, err := json.Marshal(req.Config)
	if err != nil {
		return cfg, fmt.Errorf("%w: config: %w", ErrRequest, err)
	}

	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("%w: config: %w", ErrRequest, err)
	}

	return cfg, nil
}

// Detect answers detect: a go.mod that requires GORM is a manifest, a Go file
// that imports it a source.
func Detect(_ context.Context, in []byte, _ string) (any, error) {
	var req plugin.DetectRequest
	if err := decode(in, &req); err != nil {
		return nil, err
	}

	ans := plugin.DetectAnswer{Claims: []plugin.Claim{}}

	for _, p := range req.Paths {
		b, err := os.ReadFile(filepath.Join(req.Tree, filepath.FromSlash(p)))
		if err != nil {
			continue
		}

		role, ok := analysis.DetectFile(p, b)
		if !ok {
			continue
		}

		r := RoleSource

		switch role {
		case analysis.RoleModule:
			r = RoleManifest
		case analysis.RoleModuleFile:
			r = RoleModuleFile
		}

		ans.Claims = append(ans.Claims, plugin.Claim{Path: p, Role: r})
	}

	return ans, nil
}

// modules are the module directories to answer for: the config's, else those
// of claimed manifests, else those found in the tree.
func modules(req plugin.TreeRequest, cfg analysis.Config) ([]string, error) {
	mods := cfg.Modules

	switch {
	case len(mods) > 0:
	case req.Claimed != nil:
		for _, c := range req.Claimed {
			if c.Role == RoleManifest && path.Base(c.Path) == "go.mod" {
				mods = append(mods, path.Dir(c.Path))
			}
		}
	default:
		found, err := analysis.DetectModules(req.Tree)
		if err != nil {
			return nil, fmt.Errorf("finding modules: %w", err)
		}

		mods = found
	}

	if len(req.Paths) == 0 {
		return mods, nil
	}

	// A module's set also holds the directories its go.mod replaces dependencies
	// with and the root's go.work, so every set that holds a path is answered.
	var keep []string

	for _, m := range mods {
		// a deleted module has no go.mod to read; its own paths still
		// answer its (empty) set
		replaces, err := analysis.LocalReplaces(req.Tree, []string{m})
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("finding modules: %w", err)
		}

		dirs := append([]string{m}, replaces...)

		if slices.ContainsFunc(req.Paths, func(p string) bool {
			return p == "go.work" || slices.ContainsFunc(dirs, func(d string) bool { return within(d, p) })
		}) {
			keep = append(keep, m)
		}
	}

	return keep, nil
}

// within says a path is in a module's directory, nested modules included,
// which is only more work.
func within(mod, p string) bool {
	return mod == "." || p == mod || strings.HasPrefix(p, mod+"/")
}

// Links answers links, one set per module.
func Links(ctx context.Context, in []byte, version string) (any, error) {
	var req plugin.TreeRequest
	if err := decode(in, &req); err != nil {
		return nil, err
	}

	cfg, err := config(req)
	if err != nil {
		return nil, err
	}

	mods, err := modules(req, cfg)
	if err != nil {
		return nil, err
	}

	ans := plugin.LinksAnswer{Sets: []plugin.Set{}}
	total := 0

	for _, m := range mods {
		s, complete, err := moduleSet(ctx, req, cfg, m, version)
		if err != nil {
			return nil, err
		}

		if !complete {
			no := false
			ans.Cacheable = &no
		}

		total += len(s.Links)
		ans.Sets = append(ans.Sets, s)
	}

	if total > inlineLinks {
		name, err := writeSets(req.Work, "sets.jsonl", ans.Sets)
		if err != nil {
			return nil, err
		}

		ans.Files, ans.Sets = []string{name}, nil
	}

	return ans, nil
}

// moduleSet analyses one module. A module the module cache or the toolchain
// can't load answers its GORM-importing declarations as cannot tell, and
// complete is false.
func moduleSet(ctx context.Context, req plugin.TreeRequest, cfg analysis.Config, mod, version string) (plugin.Set, bool, error) {
	s := plugin.Set{Group: mod, Inputs: []string{}, Links: []plugin.Link{}}

	if _, err := os.Stat(filepath.Join(req.Tree, mod, "go.mod")); errors.Is(err, os.ErrNotExist) {
		s.Inputs = []string{path.Join(mod, "go.mod")}

		return s, true, nil
	}

	cfg.Modules = []string{mod}

	replaces, err := analysis.LocalReplaces(req.Tree, []string{mod})
	if err != nil {
		return s, false, fmt.Errorf("analysing %s: %w", mod, err)
	}

	inputs, err := moduleInputs(req, mod, replaces)
	if err != nil {
		return s, false, fmt.Errorf("analysing %s: %w", mod, err)
	}

	r, err := analyze(ctx, req, cfg, version)

	switch {
	case errors.Is(err, analysis.ErrIncomplete) || errors.Is(err, analysis.ErrToolchain):
		fmt.Fprintln(os.Stderr, "gorm-correlator:", err)

		reason := analysis.ErrIncomplete.Error()
		if errors.Is(err, analysis.ErrToolchain) {
			reason = analysis.ErrToolchain.Error()
		}

		cts, ierr := analysis.Unreadable(req.Tree, mod, reason, inputs)
		if ierr != nil {
			return s, false, fmt.Errorf("analysing %s: %w", mod, ierr)
		}

		s.Inputs = inputs
		for _, c := range cts {
			s.CannotTell = append(s.CannotTell, plugin.CannotTell{Unit: c.Unit, Reason: strings.Join(c.Reasons, "; ")})
		}

		return s, false, nil
	case err != nil:
		return s, false, err
	}

	s.Inputs = inputs

	for _, l := range r.Links {
		s.Links = append(s.Links, plugin.Link{From: l.From, To: l.To, Kind: l.Kind, Narrowing: l.Narrowing, While: l.While, Ref: l.Reference()})
	}

	for _, c := range r.CannotTell {
		s.CannotTell = append(s.CannotTell, plugin.CannotTell{Unit: c.Unit, Reason: strings.Join(c.Reasons, "; ")})
	}

	return s, true, nil
}

func moduleInputs(req plugin.TreeRequest, mod string, replaces []string) ([]string, error) {
	if req.Claimed == nil {
		inputs, err := analysis.ModuleInputs(req.Tree, mod, replaces)
		if err != nil {
			return nil, fmt.Errorf("listing inputs: %w", err)
		}

		return inputs, nil
	}

	claimed := make([]string, 0, len(req.Claimed))
	for _, c := range req.Claimed {
		claimed = append(claimed, c.Path)
	}

	return analysis.ClaimedInputs(claimed, mod, replaces), nil
}

func writeSets(work, name string, sets []plugin.Set) (string, error) {
	var b strings.Builder

	enc := json.NewEncoder(&b)
	for _, s := range sets {
		if err := enc.Encode(s); err != nil {
			return "", fmt.Errorf("writing sets: %w", err)
		}
	}

	if err := os.WriteFile(filepath.Join(work, name), []byte(b.String()), 0o600); err != nil {
		return "", fmt.Errorf("writing sets: %w", err)
	}

	return name, nil
}

// Why answers why: the places in the code that made a link.
func Why(ctx context.Context, in []byte, version string) (any, error) {
	var req WhyRequest
	if err := decode(in, &req); err != nil {
		return nil, err
	}

	cfg, err := config(req.TreeRequest)
	if err != nil {
		return nil, err
	}

	r, err := analyze(ctx, req.TreeRequest, cfg, version)
	if err != nil {
		return nil, err
	}

	for l, where := range r.Evidence {
		if l.From == req.From && l.To == req.To {
			note := l.Kind + " at " + strings.Join(where, ", ")
			if l.While != "" {
				note += " (while " + l.Narrowing + " is " + l.While + ")"
			}

			return WhyAnswer{Path: []Step{{Unit: l.From, Note: note}, {Unit: l.To}}}, nil
		}
	}

	return nil, plugin.ErrCantTell
}

// Inspect answers the inspect call: the whole result, models and counts
// included.
func Inspect(ctx context.Context, in []byte, version string) (any, error) {
	var req plugin.TreeRequest
	if err := decode(in, &req); err != nil {
		return nil, err
	}

	cfg, err := config(req)
	if err != nil {
		return nil, err
	}

	return analyze(ctx, req, cfg, version)
}

func analyze(ctx context.Context, req plugin.TreeRequest, cfg analysis.Config, version string) (*analysis.Result, error) {
	// a development build's parser may differ from the one that wrote the
	// cache, so it parses everything again
	cache := req.Cache
	if version == "dev" || version == "" {
		cache = ""
	}

	known := loadCache(cache, version)
	parser := sqlrefs.NewParser(known)

	if len(known) == 0 {
		sqlrefs.Warm()
	}

	r, err := analysis.Analyze(ctx, req.Tree, cfg, analysis.Options{Parser: parser})
	if err != nil {
		return nil, fmt.Errorf("analysing %s: %w", req.Tree, err)
	}

	saveCache(cache, version, parser)

	return r, nil
}

func cacheFile(dir, version string) string {
	return filepath.Join(dir, "sqlrefs-"+strings.NewReplacer("/", "_", " ", "_").Replace(version)+".json")
}

func loadCache(dir, version string) map[string]sqlrefs.Result {
	if dir == "" {
		return nil
	}

	b, err := os.ReadFile(cacheFile(dir, version))
	if err != nil {
		return nil
	}

	var known map[string]sqlrefs.Result
	if json.Unmarshal(b, &known) != nil {
		// a damaged cache is parsed again
		return nil
	}

	return known
}

// saveCache writes the parse cache under a temporary name and renames it, so
// concurrent runs never read half a file. A failure only costs time.
func saveCache(dir, version string, p *sqlrefs.Parser) {
	known, dirty := p.Known()
	if dir == "" || !dirty {
		return
	}

	if err := writeCache(dir, version, known); err != nil {
		fmt.Fprintln(os.Stderr, "gorm-correlator: keeping the SQL cache:", err)
	}
}

func writeCache(dir, version string, known map[string]sqlrefs.Result) error {
	b, err := json.Marshal(known)
	if err != nil {
		return fmt.Errorf("encoding: %w", err)
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("making %s: %w", dir, err)
	}

	f, err := os.CreateTemp(dir, ".sqlrefs-*")
	if err != nil {
		return fmt.Errorf("writing: %w", err)
	}

	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}

	if err == nil {
		err = os.Rename(f.Name(), cacheFile(dir, version))
	}

	if err != nil {
		_ = os.Remove(f.Name()) //nolint:errcheck // a leftover temporary file costs nothing

		return fmt.Errorf("writing: %w", err)
	}

	return nil
}
