package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen-sbom-go/decls"
	"github.com/sideways-works/pipelinegen-sbom-go/detect"
	"github.com/sideways-works/pipelinegen-sbom-go/goid"
	"github.com/sideways-works/pipelinegen/plugin"
)

// Exit codes the protocol defines.
const (
	exitOK         = 0
	exitFailed     = 1
	exitCannotTell = 3
)

const (
	callDescribe = "describe"
	callDetect   = "detect"
	callUnits    = "units"
	callDocument = "document"
)

type Config struct {
	// Modules are the directories of the Go modules to list, relative to
	// the tree; none lists every go.mod under it.
	Modules    []string           `json:"modules"`
	DigestFrom []decls.DigestFrom `json:"digest_from"`
	Build      decls.Build        `json:"build"`
	// GoModCache is the module cache to read dependencies' go.mod files
	// from; empty is GOMODCACHE, else GOPATH/pkg/mod.
	GoModCache string `json:"gomodcache"`
}

type Request struct {
	Protocol   int      `json:"protocol"`
	Work       string   `json:"work"`
	Cache      string   `json:"cache"`
	Tree       string   `json:"tree"`
	Config     Config   `json:"config"`
	Paths      []string `json:"paths"`
	Narrowings []string `json:"narrowings"`
	// Claimed are the files the core passes as this plugin's, after the
	// repository's include and exclude patterns.
	Claimed []Claimed `json:"claimed"`
}

type Claimed struct {
	Path string `json:"path"`
	Role string `json:"role"`
}

var (
	errUsage      = errors.New("usage: go-sbom describe|detect|units|document < request.json")
	errRequest    = errors.New("request")
	errCannotTell = errors.New("can't tell")
)

func Run(args []string, stdin io.Reader, stdout, stderr io.Writer, version string) int {
	if len(args) != 1 {
		say(stderr, errUsage)

		return exitFailed
	}

	answer, err := call(args[0], stdin, version)
	if err != nil {
		say(stderr, "go-sbom:", err)

		if errors.Is(err, errCannotTell) {
			return exitCannotTell
		}

		return exitFailed
	}

	if err := json.NewEncoder(stdout).Encode(answer); err != nil {
		say(stderr, "go-sbom:", err)

		return exitFailed
	}

	return exitOK
}

func call(name string, stdin io.Reader, version string) (any, error) {
	if name == callDescribe {
		return describe(version), nil
	}

	var req Request
	if err := json.NewDecoder(stdin).Decode(&req); err != nil {
		return nil, fmt.Errorf("%w: %w", errRequest, err)
	}

	if req.Protocol != 1 {
		return nil, fmt.Errorf("%w: protocol %d, this plugin speaks 1", errRequest, req.Protocol)
	}

	if req.Tree == "" {
		return nil, fmt.Errorf("%w: no tree", errRequest)
	}

	switch name {
	case callDetect:
		return detectCall(req)
	case callUnits:
		return units(req)
	case callDocument:
		return document(req, version)
	default:
		return nil, fmt.Errorf("%w: unknown call %q", errUsage, name)
	}
}

// say writes a line for people; a failed write has nobody left to tell.
func say(w io.Writer, a ...any) {
	if _, err := fmt.Fprintln(w, a...); err != nil {
		return
	}
}

// The answer's types are the protocol's own.
type (
	Unit       = plugin.Unit
	Link       = plugin.Link
	CannotTell = plugin.CannotTell
	Set        = plugin.Set
	// UnitsAnswer answers units: one set per package and one for the dependencies.
	UnitsAnswer = plugin.UnitsAnswer
)

func units(req Request) (UnitsAnswer, error) {
	tree, err := decls.Root(req.Tree)
	if err != nil {
		return UnitsAnswer{}, fmt.Errorf("%w: %w", errRequest, err)
	}

	mods, err := modules(tree, req.Config.Modules, req.Claimed)
	if err != nil {
		return UnitsAnswer{}, err
	}

	dirs, err := packageDirs(tree, mods, req.Paths, req.Claimed)
	if err != nil {
		return UnitsAnswer{}, err
	}

	cfg := decls.Config{DigestFrom: req.Config.DigestFrom, Build: req.Config.Build}
	out := UnitsAnswer{Sets: []Set{}}

	for _, dir := range dirs {
		s, err := packageSet(tree, dir, cfg)
		if err != nil {
			return UnitsAnswer{}, err
		}

		out.Sets = append(out.Sets, s)
	}

	if req.Paths == nil || slices.ContainsFunc(req.Paths, isDepsInput) {
		s, err := depsSet(tree, mods, req.Config)
		if err != nil {
			return UnitsAnswer{}, err
		}

		out.Sets = append(out.Sets, s)

		if len(s.CannotTell) > 0 {
			// the answer depends on the module cache, not only on the tree
			no := false
			out.Cacheable = &no
		}
	}

	return out, nil
}

// modules are the module directories to list: the configured ones, else
// those of the claimed go.mod files, else every go.mod of the tree.
func modules(tree string, configured []string, claimed []Claimed) ([]string, error) {
	if len(configured) == 0 && claimed != nil {
		var out []string

		for _, c := range claimed {
			if path.Base(c.Path) == fileGoMod {
				out = append(out, path.Dir(path.Clean(c.Path)))
			}
		}

		slices.Sort(out)

		return slices.Compact(out), nil
	}

	if len(configured) == 0 {
		mods, err := decls.Modules(tree)
		if err != nil {
			return nil, fmt.Errorf("units: %w", err)
		}

		if len(mods) == 0 {
			// nothing claimed the files and the walk found no module: an
			// empty list would read as every Go unit gone
			return nil, fmt.Errorf("%w: no go.mod under %s", errCannotTell, tree)
		}

		return mods, nil
	}

	out := make([]string, 0, len(configured))

	for _, m := range configured {
		m = path.Clean(filepath.ToSlash(m))
		if _, err := os.Stat(filepath.Join(tree, m, fileGoMod)); err != nil {
			return nil, fmt.Errorf("%w: module %s: %w", errRequest, m, err)
		}

		out = append(out, m)
	}

	slices.Sort(out)

	return slices.Compact(out), nil
}

// packageDirs are the package directories to answer for: every one of the
// modules, or those the request's paths may change.
func packageDirs(tree string, mods, paths []string, claimed []Claimed) ([]string, error) {
	if paths == nil {
		keep := map[string]bool{}
		for _, c := range claimed {
			keep[path.Dir(path.Clean(c.Path))] = true
		}

		var out []string

		for _, m := range mods {
			dirs, err := decls.Dirs(tree, m)
			if err != nil {
				return nil, fmt.Errorf("units: %w", err)
			}

			for _, d := range dirs {
				if claimed == nil || keep[d] {
					out = append(out, d)
				}
			}
		}

		return out, nil
	}

	seen := map[string]bool{}

	var out []string

	for _, p := range paths {
		for _, dir := range candidates(tree, mods, path.Clean(filepath.ToSlash(p))) {
			if !seen[dir] {
				seen[dir] = true
				out = append(out, dir)
			}
		}
	}

	slices.Sort(out)

	return out, nil
}

// candidates are the package directories a changed path may belong to: a
// Go file's own directory (even when it is gone, so its answer empties),
// and for any other file every package directory above it within its
// module, since a package may embed it.
func candidates(tree string, mods []string, p string) []string {
	mod := moduleOf(tree, mods, p)
	if mod == "" {
		return nil
	}

	dir := path.Dir(p)
	if hidden(mod, dir) {
		return nil
	}

	if strings.HasSuffix(p, ".go") {
		return []string{dir}
	}

	var out []string

	for d := dir; ; d = path.Dir(d) {
		if decls.HasGo(filepath.Join(tree, d)) {
			out = append(out, d)
		}

		if d == mod || d == "." {
			return out
		}
	}
}

func moduleOf(tree string, mods []string, p string) string {
	for d := path.Dir(p); ; d = path.Dir(d) {
		if _, err := os.Stat(filepath.Join(tree, d, fileGoMod)); err == nil {
			if slices.Contains(mods, d) {
				return d
			}

			return ""
		}

		if d == "." {
			return ""
		}
	}
}

func hidden(mod, dir string) bool {
	rest := strings.TrimPrefix(dir, mod)
	if mod == "." {
		rest = dir
	}

	for part := range strings.SplitSeq(rest, "/") {
		if part != "" && part != "." && decls.Skip(part) {
			return true
		}
	}

	return false
}

// packageSet reads one package directory into its set; a directory without
// Go files any more answers an empty set, so the core drops what it had.
func packageSet(tree, dir string, cfg decls.Config) (Set, error) {
	s := Set{Group: dir, Inputs: []string{}, Units: []Unit{}, Links: []Link{}}
	if !decls.HasGo(filepath.Join(tree, dir)) {
		return s, nil
	}

	p, err := decls.ReadWith(tree, dir, cfg)
	if err != nil {
		return Set{}, fmt.Errorf("units: %w", err)
	}

	s.Inputs, s.Whole = p.Inputs, p.Whole != ""

	// a test type with a typedecl twin is rooted at the twin: the type
	// itself carries its method set, which a slice binds only through links
	twins := map[goid.ID]bool{}

	for _, u := range p.Units {
		if u.ID.Kind() == goid.TypeDecl && u.ID.Test() {
			twins[goid.New(u.ID.Dir(), goid.Type, u.ID.Name())] = true
		}
	}

	// while unexported-methods is off, a type binds the signatures of the
	// unexported methods its digest leaves out
	for _, typ := range slices.Sorted(maps.Keys(p.Private)) {
		for _, sig := range p.Private[typ] {
			s.Links = append(s.Links, Link{From: string(typ), To: string(sig), Kind: plugin.LinkUses, Narrowing: NarrowUnexportedMethods, While: plugin.WhileOff})
		}
	}

	for _, u := range p.Units {
		s.Units = append(s.Units, Unit{
			ID: string(u.ID), Group: dir, Digest: u.Digest, DigestFrom: u.DigestFrom,
			File: u.File, Kind: string(u.ID.Kind()), Executable: u.Executable,
			Entry: entry(u.ID), NotRoot: twins[u.ID], Label: labelOf(u.ID, p.Name()),
		})

		// a package that doesn't parse has files for units: what each of
		// them declares, and so what reaches it, can't be told
		if p.Unparsed != "" {
			s.CannotTell = append(s.CannotTell, CannotTell{Unit: string(u.ID), Reason: NotBuilding(dir, cfg.Build, p.Unparsed)})
		}
	}

	return s, nil
}

const (
	fileGoMod = "go.mod"
	fileGoSum = "go.sum"
)

// NarrowUnexportedMethods leaves out of a type's digest the unexported
// methods nothing in the package can reach but a call that names them.
const NarrowUnexportedMethods = "unexported-methods"

// unexportedMethodsOffAt is the level that switches it off: the top of the
// ladder, with the other narrowings that rest on the language alone.
const unexportedMethodsOffAt = 5

// labelOf names a unit for people. It is a variable so a test can show that
// nothing else in an answer depends on it.
var labelOf = goid.Label

// NotBuilding is the reason given for a package the build can't compile:
// what is wrong, and what it costs until it is fixed.
func NotBuilding(dir string, b decls.Build, first string) string {
	return fmt.Sprintf("package %s does not build with tags [%s]: %s; tests that reach it run on every change until it builds or is excluded",
		dir, strings.Join(b.Context().BuildTags, " "), first)
}

// entry is the name a test runner selects a test file's function by:
// Test, Benchmark, Fuzz and Example functions, never TestMain, which runs
// for every test of the binary.
func entry(id goid.ID) string {
	if id.Kind() != goid.Func || !id.Test() {
		return ""
	}

	n := strings.TrimSuffix(strings.TrimSuffix(id.Name(), goid.XTestSuffix), goid.TestSuffix)
	if n == "TestMain" {
		return ""
	}

	for _, prefix := range []string{"Test", "Benchmark", "Fuzz", "Example"} {
		if strings.HasPrefix(n, prefix) {
			return n
		}
	}

	return ""
}

func describe(version string) plugin.Description {
	return plugin.Description{
		Protocol: plugin.Protocol, Kind: plugin.KindSBOM, Name: "go-sbom", Version: version,
		Namespaces:   []string{goid.Namespace, goid.DepNamespace},
		Capabilities: []string{callDetect, callUnits, callDocument},
		Candidates:   detect.Candidates,
		Inputs:       map[string]string{callDetect: plugin.PerFile, callUnits: plugin.PerSet, callDocument: plugin.PerSet},
		Narrowings: []plugin.Narrowing{{
			Name: NarrowModuleGraph, OffAt: moduleGraphOffAt,
			Note: "sound next to the Go correlator: a module's own requirements bind nothing by themselves while it links each package " +
				"to the module of every package it imports, transitively: a required module no linked package imports " +
				"isn't compiled in. Without that correlator nothing links code to modules, so leave this narrowing off",
		}, {
			Name: NarrowUnexportedMethods, OffAt: unexportedMethodsOffAt,
			Note: "sound: an unexported method is seen only by its own package; reflection and templates see exported methods alone, " +
				"and an interface, a field or another type's method of the same name in the package keeps it in the type's digest. " +
				"Without such a name only code that calls the method by name can reach it, and that code changes with it",
		}},
		Needs:  []string{},
		Caches: []string{"gomodcache"},
		// on a person's machine the core fills the module cache before
		// planning; the plugin itself never reaches the network
		Fill: map[string]plugin.Fill{"gomodcache": {Files: []string{fileGoMod, fileGoSum}, Run: []string{"go", "mod", "download"}}},
	}
}

// detectCall claims the request's paths that are the Go plugins' files. A tree
// that is gone fails, since every path would be left out.
func detectCall(req Request) (any, error) {
	tree, err := decls.Root(req.Tree)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRequest, err)
	}

	out := plugin.DetectAnswer{Claims: []plugin.Claim{}}

	for _, p := range req.Paths {
		p = path.Clean(filepath.ToSlash(p))

		b, err := os.ReadFile(filepath.Join(tree, p)) //nolint:gosec // a path of the tree the core asks about
		if err != nil {
			continue
		}

		if c, ok := detect.File(p, b); ok {
			out.Claims = append(out.Claims, plugin.Claim{Path: c.Path, Role: c.Role})
		}
	}

	return out, nil
}
