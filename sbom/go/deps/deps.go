// Package deps lists the modules a repository's Go code depends on and the
// toolchain it builds with, from files alone: it never runs the go command or
// reaches the network.
package deps

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	"github.com/sideways-works/pipelinegen-sbom-go/decls"
	"github.com/sideways-works/pipelinegen-sbom-go/goid"
)

type Module struct {
	// ID is its unit: dep:go:<path>.
	ID   string
	Path string
	// Version is the version built: the highest any main module requires.
	Version string
	// Replace is the module that replaces it, as path@version, if any.
	Replace string
	// Sum is the h1 checksum of the module's (or its replacement's) zip
	// from go.sum, empty when go.sum lacks it.
	Sum string
	// Indirect is true when every main module marks it // indirect.
	Indirect bool
	// Digest changes when the version, the replacement or the checksum a
	// main module builds with changes.
	Digest string
}

// Tool is one toolchain unit: the go directive, the toolchain directive or
// the Go SDK Bazel builds with.
type Tool struct {
	ID      string
	Version string
	Digest  string
}

// Link is a requirement of one module's go.mod on another module.
type Link struct {
	From, To string
}

type Missing struct {
	ID, Reason string
}

type Graph struct {
	Modules []Module
	Tools   []Tool
	// Links are the module graph, among Modules only.
	Links []Link
	// CannotTell are the modules whose own go.mod isn't in the module
	// cache: their links are unknown, never empty.
	CannotTell []Missing
	Inputs     []string
	Mains      []Main
}

type Main struct {
	Dir, Path string
	Requires  []string
}

var ErrRead = errors.New("dependencies")

type mainModule struct {
	dir   string
	file  *modfile.File
	scope string // the go.work using it, else its own directory
	sums  map[string]string
}

// Read reads the dependencies of the main modules in mods (directories
// relative to tree). cache is the module cache's directory (GOMODCACHE).
func Read(tree string, mods []string, cache string) (*Graph, error) {
	tree, err := decls.Root(tree)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRead, err)
	}

	r := &reader{tree: tree, g: &Graph{}, firstParty: map[string]bool{}}

	if err := r.firstPartyModules(); err != nil {
		return nil, err
	}

	works, err := r.workspaces()
	if err != nil {
		return nil, err
	}

	var mains []*mainModule

	for _, dir := range mods {
		m, err := r.readMain(dir, works)
		if err != nil {
			return nil, err
		}

		mains = append(mains, m)
	}

	r.modules(mains, works)

	units := map[string]bool{}
	for _, m := range r.g.Modules {
		units[m.ID] = true
	}

	for _, m := range mains {
		main := Main{Dir: m.dir, Path: m.file.Module.Mod.Path}

		for _, req := range m.file.Require {
			if id := goid.Dep(req.Mod.Path); units[id] {
				main.Requires = append(main.Requires, id)
			}
		}

		slices.Sort(main.Requires)
		r.g.Mains = append(r.g.Mains, main)
	}

	r.tools(mains, works)

	if err := r.bazelSDK(); err != nil {
		return nil, err
	}

	r.links(cache)

	slices.Sort(r.g.Inputs)
	r.g.Inputs = slices.Compact(r.g.Inputs)

	return r.g, nil
}

type reader struct {
	tree string
	g    *Graph
	// firstParty are the module paths of every go.mod in the tree: their
	// code is in the tree, not a dependency.
	firstParty map[string]bool
}

func (r *reader) read(rel string) ([]byte, bool, error) {
	b, err := os.ReadFile(filepath.Join(r.tree, rel)) //nolint:gosec // a module file of the tree, by a path the reader built
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, fmt.Errorf("%w: %w", ErrRead, err)
	}

	r.g.Inputs = append(r.g.Inputs, rel)

	return b, true, nil
}

func (r *reader) firstPartyModules() error {
	dirs, err := decls.Modules(r.tree)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrRead, err)
	}

	for _, d := range dirs {
		b, _, err := r.read(path.Join(d, "go.mod"))
		if err != nil {
			return err
		}

		if p := modfile.ModulePath(b); p != "" {
			r.firstParty[p] = true
		}
	}

	return nil
}

type workspace struct {
	file string // relative to the tree
	work *modfile.WorkFile
	uses map[string]bool // module directories, relative to the tree
	sums map[string]string
}

func (r *reader) workspaces() ([]*workspace, error) {
	var files []string

	err := filepath.WalkDir(r.tree, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if e.IsDir() && p != r.tree && decls.Skip(e.Name()) {
			return filepath.SkipDir
		}

		if !e.IsDir() && e.Name() == "go.work" {
			rel, err := filepath.Rel(r.tree, p)
			if err != nil {
				return fmt.Errorf("%w: %w", ErrRead, err)
			}

			files = append(files, filepath.ToSlash(rel))
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRead, err)
	}

	slices.Sort(files)

	out := make([]*workspace, 0, len(files))

	for _, f := range files {
		b, _, err := r.read(f)
		if err != nil {
			return nil, err
		}

		wf, err := modfile.ParseWork(f, b, nil)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrRead, err)
		}

		w := &workspace{file: f, work: wf, uses: map[string]bool{}, sums: map[string]string{}}
		for _, u := range wf.Use {
			w.uses[path.Clean(path.Join(path.Dir(f), filepath.ToSlash(u.Path)))] = true
		}

		if err := r.sums(path.Join(path.Dir(f), "go.work.sum"), w.sums); err != nil {
			return nil, err
		}

		out = append(out, w)
	}

	return out, nil
}

func (r *reader) readMain(dir string, works []*workspace) (*mainModule, error) {
	rel := path.Join(dir, "go.mod")

	b, ok, err := r.read(rel)
	if err != nil {
		return nil, err
	}

	if !ok {
		return nil, fmt.Errorf("%w: %s: no go.mod", ErrRead, dir)
	}

	f, err := modfile.Parse(rel, b, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRead, err)
	}

	if f.Module == nil {
		return nil, fmt.Errorf("%w: %s has no module line", ErrRead, rel)
	}

	m := &mainModule{dir: dir, file: f, scope: dir, sums: map[string]string{}}

	for _, w := range works {
		if w.uses[dir] {
			m.scope = w.file

			break
		}
	}

	if err := r.sums(path.Join(dir, "go.sum"), m.sums); err != nil {
		return nil, err
	}

	if err := r.vendored(m); err != nil {
		return nil, err
	}

	return m, nil
}

// sums reads a go.sum's zip checksums, by path@version; the /go.mod lines
// are left out, so a tidy that adds only those changes nothing.
func (r *reader) sums(rel string, into map[string]string) error {
	b, _, err := r.read(rel)
	if err != nil {
		return err
	}

	for line := range strings.Lines(string(b)) {
		f := strings.Fields(line)
		if len(f) != 3 || strings.HasSuffix(f[1], "/go.mod") {
			continue
		}

		into[f[0]+"@"+f[1]] = f[2]
	}

	return nil
}

// vendorLine is a module line of vendor/modules.txt: `# path version` or
// `# path version => replacement [version]`.
var vendorLine = regexp.MustCompile(`^# (\S+) (v\S+)`)

// vendored adds the modules vendor/modules.txt lists that go.mod doesn't,
// at the version vendored; the go command refuses a vendor directory that
// disagrees with go.mod, so for the others go.mod stands.
func (r *reader) vendored(m *mainModule) error {
	b, ok, err := r.read(path.Join(m.dir, "vendor", "modules.txt"))
	if err != nil || !ok {
		return err
	}

	listed := map[string]bool{}
	for _, req := range m.file.Require {
		listed[req.Mod.Path] = true
	}

	for line := range strings.Lines(string(b)) {
		sub := vendorLine.FindStringSubmatch(strings.TrimSpace(line))
		if sub == nil || listed[sub[1]] {
			continue
		}

		listed[sub[1]] = true
		m.file.Require = append(m.file.Require, &modfile.Require{Mod: module.Version{Path: sub[1], Version: sub[2]}, Indirect: true})
	}

	return nil
}

type choice struct {
	scope, version, replace, sum string
	indirect                     bool
}

func (r *reader) modules(mains []*mainModule, works []*workspace) {
	byPath := map[string]map[string]*choice{}
	repl := map[string]replaceSet{}

	for _, m := range mains {
		repl[m.scope] = replacements(m, mains, works)

		for _, req := range m.file.Require {
			p := req.Mod.Path
			if r.firstParty[p] {
				continue
			}

			if byPath[p] == nil {
				byPath[p] = map[string]*choice{}
			}

			c := byPath[p][m.scope]
			if c == nil {
				c = &choice{scope: m.scope, indirect: true}
				byPath[p][m.scope] = c
			}

			// a workspace builds the highest version any of its modules requires
			if c.version == "" || semver.Compare(req.Mod.Version, c.version) > 0 {
				c.version = req.Mod.Version
			}

			c.indirect = c.indirect && req.Indirect
		}
	}

	for p, scopes := range byPath {
		for s, c := range scopes {
			c.replace, _ = repl[s].lookup(p, c.version)
		}
	}

	sums := scopeSums(mains, works)

	for _, p := range sortedKeys(byPath) {
		mod, ok := r.module(p, byPath[p], sums)
		if ok {
			r.g.Modules = append(r.g.Modules, mod)
		}
	}
}

// module makes one dependency's unit from what each scope builds; a module
// that every scope replaces with a directory is first-party code: no unit.
func (r *reader) module(p string, scopes map[string]*choice, sums map[string]map[string]string) (Module, bool) {
	mod := Module{ID: goid.Dep(p), Path: p, Indirect: true}
	h := sha256.New()
	local := true

	for _, s := range sortedKeys(scopes) {
		c := scopes[s]
		if c.replace == localReplace {
			continue
		}

		local = false

		built := p + "@" + c.version
		if c.replace != "" {
			built = c.replace
		}

		c.sum = sums[s][built]

		if mod.Version == "" || semver.Compare(c.version, mod.Version) > 0 {
			mod.Version, mod.Replace, mod.Sum = c.version, c.replace, c.sum
		}

		mod.Indirect = mod.Indirect && c.indirect

		for _, part := range []string{s, c.version, c.replace, c.sum} {
			h.Write([]byte(part))
			h.Write([]byte{0})
		}
	}

	mod.Digest = hex.EncodeToString(h.Sum(nil))

	return mod, !local
}

const localReplace = "local"

type replaceSet []*modfile.Replace

func (rs replaceSet) lookup(p, version string) (string, bool) {
	var found *modfile.Replace

	for _, rp := range rs {
		if rp.Old.Path != p {
			continue
		}

		// a replace of one version beats one of every version; among
		// equals the first wins
		switch {
		case rp.Old.Version == version && (found == nil || found.Old.Version == ""):
			found = rp
		case rp.Old.Version == "" && found == nil:
			found = rp
		}
	}

	if found == nil {
		return "", false
	}

	if found.New.Version == "" {
		return localReplace, true
	}

	return found.New.Path + "@" + found.New.Version, true
}

// replacements are the replace directives in force for a main module: its
// own, or in a workspace the go.work's first, so they win, then those of
// every module it uses.
func replacements(m *mainModule, mains []*mainModule, works []*workspace) replaceSet {
	if m.scope == m.dir {
		return m.file.Replace
	}

	var out replaceSet

	for _, o := range mains {
		if o.scope == m.scope {
			out = append(out, o.file.Replace...)
		}
	}

	for _, w := range works {
		if w.file == m.scope {
			out = append(slices.Clone(w.work.Replace), out...)
		}
	}

	return out
}

func scopeSums(mains []*mainModule, works []*workspace) map[string]map[string]string {
	out := map[string]map[string]string{}

	add := func(scope string, sums map[string]string) {
		if out[scope] == nil {
			out[scope] = map[string]string{}
		}

		maps.Copy(out[scope], sums)
	}

	for _, m := range mains {
		add(m.scope, m.sums)
	}

	for _, w := range works {
		add(w.file, w.sums)
	}

	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	slices.Sort(out)

	return out
}
