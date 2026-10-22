package npm

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Namespace is the prefix of every unit id this package makes.
const Namespace = "dep:npm:"

// Unit kinds.
const (
	KindPackage = "package"
	// KindToolchain is a unit that reaches everything built or run with
	// it: pipelinegen samples every suite that uses it instead of
	// following links.
	KindToolchain = "toolchain"
)

// LinkUses is the kind of a link from a package to one it depends on.
const LinkUses = "uses"

// DefaultToolchains are the packages that build, check or run code rather
// than being loaded by it. A pattern ending in /* takes a whole scope.
var DefaultToolchains = []string{
	"typescript", "esbuild", "vite", "rolldown", "rollup", "webpack", "@vitejs/*", "@swc/*",
	"@biomejs/biome", "eslint", "prettier",
	"@playwright/test", "playwright", "playwright-core", "vitest", "jest",
}

// Config is what a repository says about its npm workspaces.
type Config struct {
	// Lockfiles are the lockfiles' paths in the tree; when empty, every
	// lockfile detection finds.
	Lockfiles []string `json:"lockfiles,omitempty"`
	// Toolchains replaces DefaultToolchains when set.
	Toolchains []string `json:"toolchains,omitempty"`
	// Documents are CycloneDX or SPDX JSON documents whose npm packages
	// count too, for packages no lockfile lists.
	Documents []string `json:"documents,omitempty"`
}

// A Unit is something a change can touch and a test can depend on.
type Unit struct {
	ID         string `json:"id"`
	Group      string `json:"group"`
	Digest     string `json:"digest"`
	File       string `json:"file"`
	Kind       string `json:"kind"`
	Executable bool   `json:"executable"`
	// Attributes are what else is known about the unit, for documents and
	// reports; selection doesn't use them.
	Attributes Attributes `json:"attributes"`
	// Label is the unit as people name it ("the package react@18.3.1"),
	// for pipelinegen's why only: nothing hashes, caches or compares it.
	Label string `json:"label,omitempty"`
}

// Attributes of a package unit.
type Attributes struct {
	// Direct says a workspace's manifest names the package.
	Direct   bool     `json:"direct"`
	Dev      bool     `json:"dev"`
	Versions []string `json:"versions"`
}

// A Link says that a test bound to From is bound to To too.
type Link struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

// Model is a tree's npm bill of materials.
type Model struct {
	Units  []Unit
	Links  []Link
	Copies map[string][]*Entry
	Inputs []string
}

// Build reads the lockfiles, toolchain versions and bill of materials
// documents into units and links. Without lockfiles in the config it detects
// them itself.
func Build(tree string, cfg Config) (*Model, error) {
	if len(cfg.Lockfiles) == 0 {
		candidates, err := Candidates(tree)
		if err != nil {
			return nil, err
		}

		cfg.Lockfiles = Lockfiles(Detect(tree, candidates))
	}

	return BuildFiles(tree, cfg)
}

// BuildFiles reads only the lockfiles and documents the config names. Without
// any it can't tell: what an install resolves is in no file.
func BuildFiles(tree string, cfg Config) (*Model, error) {
	files := cfg.Lockfiles
	if len(files) == 0 && len(cfg.Documents) == 0 {
		return nil, ErrNoLockfile
	}

	toolchains := cfg.Toolchains
	if toolchains == nil {
		toolchains = DefaultToolchains
	}

	b := &builder{copies: map[string][]*Entry{}, owner: map[string]string{}, direct: map[string]bool{}, edges: map[string]map[string]bool{}}

	for _, f := range files {
		l, err := ReadLockfile(tree, f)
		if err != nil {
			return nil, err
		}

		b.lockfile(l)
		b.inputs = append(b.inputs, f)
	}

	if err := b.documents(tree, cfg.Documents); err != nil {
		return nil, err
	}

	m := b.model(toolchains)

	tc, inputs, err := toolchainUnits(tree, files)
	if err != nil {
		return nil, err
	}

	m.Units = append(m.Units, tc...)

	for i := range m.Units {
		m.Units[i].Label = labelOf(m.Units[i])
	}

	m.Inputs = append(m.Inputs, inputs...)
	slices.Sort(m.Inputs)
	m.Inputs = slices.Compact(m.Inputs)

	return m, nil
}

type builder struct {
	copies map[string][]*Entry
	owner  map[string]string // unit id → the file that lists it
	direct map[string]bool
	edges  map[string]map[string]bool
	inputs []string
}

func (b *builder) lockfile(l *Lockfile) {
	for _, p := range l.Installed() {
		e := l.Entries[p]
		name, _ := InstallName(p)

		id := Namespace + name
		b.copies[id] = append(b.copies[id], e)

		if _, ok := b.owner[id]; !ok {
			b.owner[id] = l.File
		}

		for _, deps := range []map[string]string{e.Dependencies, e.OptionalDependencies, e.PeerDependencies} {
			for dep := range deps {
				// An optional dependency for another platform isn't
				// installed and resolves to nothing; a peer resolves to
				// what the workspace installed.
				if to, ok := l.Resolve(p, dep); ok && !l.Entries[to].Link {
					toName, _ := InstallName(to)
					b.edge(id, Namespace+toName)
				}
			}
		}
	}

	for _, w := range l.Workspaces() {
		e := l.Entries[w]
		for _, deps := range []map[string]string{e.Dependencies, e.DevDependencies, e.OptionalDependencies, e.PeerDependencies} {
			for dep := range deps {
				if to, ok := l.Resolve(w, dep); ok && !l.Entries[to].Link {
					toName, _ := InstallName(to)
					b.direct[Namespace+toName] = true
				}
			}
		}
	}
}

func (b *builder) edge(from, to string) {
	if from == to {
		return
	}

	if b.edges[from] == nil {
		b.edges[from] = map[string]bool{}
	}

	b.edges[from][to] = true
}

func (b *builder) model(toolchains []string) *Model {
	ids := make([]string, 0, len(b.copies))
	for id := range b.copies {
		ids = append(ids, id)
	}

	slices.Sort(ids)

	tools := toolchainClosure(ids, b.edges, b.direct, toolchains)
	m := &Model{Copies: b.copies, Inputs: b.inputs}

	for _, id := range ids {
		copies := b.copies[id]
		slices.SortFunc(copies, func(a, c *Entry) int { return strings.Compare(a.Path, c.Path) })

		kind := KindPackage
		if tools[id] {
			kind = KindToolchain
		}

		dev := true
		versions := []string{}

		for _, e := range copies {
			dev = dev && (e.Dev || e.DevOptional)
			versions = append(versions, e.Version)
		}

		slices.Sort(versions)

		file := b.owner[id]
		m.Units = append(m.Units, Unit{
			ID: id, Group: file, File: file, Kind: kind, Digest: digest(copies),
			Attributes: Attributes{Direct: b.direct[id], Dev: dev, Versions: slices.Compact(versions)},
		})

		tos := make([]string, 0, len(b.edges[id]))
		for to := range b.edges[id] {
			tos = append(tos, to)
		}

		slices.Sort(tos)

		for _, to := range tos {
			m.Links = append(m.Links, Link{From: id, To: to, Kind: LinkUses})
		}
	}

	return m
}

// digest covers every installed copy: install path, own name (an alias
// installs one package under another's), version and checksum. The registry
// is left out when a checksum pins the content, so a mirror changes nothing.
func digest(copies []*Entry) string {
	h := sha256.New()

	for _, e := range copies {
		where := ""
		if e.Integrity == "" {
			where = e.Resolved
		}

		h.Write([]byte(strings.Join([]string{e.Path, e.Name, e.Version, e.Integrity, where}, "\x00") + "\n"))
	}

	return hex.EncodeToString(h.Sum(nil))
}

// toolchainClosure marks the toolchain packages and every package installed
// only for them, so a bundler's own dependencies sample like the bundler
// instead of reaching every test that loads an app it built.
func toolchainClosure(ids []string, edges map[string]map[string]bool, direct map[string]bool, patterns []string) map[string]bool {
	isTool := func(id string) bool {
		name := strings.TrimPrefix(id, Namespace)

		for _, p := range patterns {
			if p == name || strings.HasSuffix(p, "/*") && strings.HasPrefix(name, strings.TrimSuffix(p, "*")) {
				return true
			}
		}

		return false
	}

	reached := map[string]bool{}

	var stack []string

	for _, id := range ids {
		if direct[id] && !isTool(id) {
			reached[id] = true
			stack = append(stack, id)
		}
	}

	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		for to := range edges[cur] {
			if !reached[to] && !isTool(to) {
				reached[to] = true
				stack = append(stack, to)
			}
		}
	}

	out := map[string]bool{}

	for _, id := range ids {
		if isTool(id) || !reached[id] && reachedFromTool(id, ids, edges, isTool) {
			out[id] = true
		}
	}

	return out
}

func reachedFromTool(id string, ids []string, edges map[string]map[string]bool, isTool func(string) bool) bool {
	seen := map[string]bool{}

	var stack []string

	for _, t := range ids {
		if isTool(t) {
			stack = append(stack, t)
			seen[t] = true
		}
	}

	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if cur == id {
			return true
		}

		for to := range edges[cur] {
			if !seen[to] {
				seen[to] = true
				stack = append(stack, to)
			}
		}
	}

	return false
}

// toolchainUnits reads the Node and npm versions the workspace declares. A
// version nobody declares is whatever the machine has, and gets no unit.
func toolchainUnits(tree string, lockfiles []string) ([]Unit, []string, error) {
	type found struct{ file, value string }

	versions := map[string][]found{}

	var inputs []string

	for _, lf := range lockfiles {
		dir := path.Dir(lf)

		for _, f := range []string{fileNvmrc, fileNodeVersion} {
			p := path.Join(dir, f)

			data, err := os.ReadFile(filepath.Join(tree, filepath.FromSlash(p))) //nolint:gosec // a file of the tree
			if errors.Is(err, os.ErrNotExist) {
				continue
			}

			if err != nil {
				return nil, nil, fmt.Errorf("toolchain: %w", err)
			}

			inputs = append(inputs, p)
			versions["node"] = append(versions["node"], found{p, strings.TrimSpace(string(data))})
		}

		manifest := path.Join(dir, fileManifest)

		m, err := readManifest(tree, manifest)
		if err != nil {
			return nil, nil, err
		}

		if m == nil {
			continue
		}

		inputs = append(inputs, manifest)

		for engine, v := range m.Engines {
			versions[engine] = append(versions[engine], found{manifest + " engines", v})
		}

		if pm, v, ok := strings.Cut(m.PackageManager, "@"); ok {
			versions[pm] = append(versions[pm], found{manifest + " packageManager", v})
		}
	}

	names := make([]string, 0, len(versions))
	for n := range versions {
		names = append(names, n)
	}

	slices.Sort(names)

	var out []Unit

	for _, n := range names {
		h := sha256.New()

		var vs []string

		for _, f := range versions[n] {
			h.Write([]byte(f.file + "\x00" + f.value + "\n"))
			vs = append(vs, f.value)
		}

		out = append(out, Unit{
			ID: ToolchainID(n), Group: versions[n][0].file, File: strings.Fields(versions[n][0].file)[0],
			Kind: KindToolchain, Digest: hex.EncodeToString(h.Sum(nil)), Attributes: Attributes{Direct: true, Versions: vs},
		})
	}

	return out, inputs, nil
}

// ToolchainID is the id of a runtime or package manager's declared version.
// A colon can't appear in an npm package name, so it never collides with a
// package's id.
func ToolchainID(name string) string {
	return Namespace + "toolchain:" + name
}
