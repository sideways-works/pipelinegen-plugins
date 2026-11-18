package cli

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/sideways-works/pipelinegen-sbom-go/deps"
	"github.com/sideways-works/pipelinegen-sbom-go/goid"
	"github.com/sideways-works/pipelinegen/plugin"
)

// NarrowModuleGraph drops the links among modules: the Go correlator
// links each package to the module of every package it imports,
// transitively, so a module required only by another module's go.mod
// isn't compiled in and can't change a result.
const NarrowModuleGraph = "module-graph"

// moduleGraphOffAt is the level that switches the module graph back on:
// the ladder's level for module bumps, of which this is the sbom's half.
const moduleGraphOffAt = 5

const depsGroup = goid.DepNamespace

const (
	kindModule    = "module"
	kindToolchain = "toolchain"
	linkRequires  = "requires"
)

func isDepsInput(p string) bool {
	switch path.Base(p) {
	case fileGoMod, fileGoSum, "go.work", "go.work.sum", "MODULE.bazel":
		return true
	case "modules.txt":
		return path.Base(path.Dir(p)) == "vendor"
	}

	return false
}

func moduleCache(cfg Config) string {
	if cfg.GoModCache != "" {
		return cfg.GoModCache
	}

	if c := os.Getenv("GOMODCACHE"); c != "" {
		return c
	}

	if gp := filepath.SplitList(os.Getenv("GOPATH")); len(gp) > 0 && gp[0] != "" {
		return filepath.Join(gp[0], "pkg", "mod")
	}

	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "go", "pkg", "mod")
	}

	return ""
}

// depsSet answers one unit per module and toolchain, with the module graph as
// links that stand while module-graph is off. A module whose go.mod the cache
// lacks is "can't tell".
func depsSet(tree string, mods []string, cfg Config) (Set, error) {
	g, err := deps.Read(tree, mods, moduleCache(cfg))
	if err != nil {
		return Set{}, fmt.Errorf("units: %w", err)
	}

	s := Set{Group: depsGroup, Inputs: g.Inputs, Units: []Unit{}, Links: []Link{}}
	if s.Inputs == nil {
		s.Inputs = []string{}
	}

	for _, m := range g.Modules {
		attrs := map[string]any{"version": m.Version}
		if m.Indirect {
			attrs["indirect"] = true
		}

		if m.Replace != "" {
			attrs["replaced_by"] = m.Replace
		}

		if m.Sum != "" {
			attrs["h1"] = m.Sum
		}

		s.Units = append(s.Units, Unit{
			ID: m.ID, Group: depsGroup, Digest: m.Digest, Kind: kindModule, Attributes: attrs,
			Label: goid.Clip("the module " + strings.TrimPrefix(m.ID, goid.DepNamespace+":") + "@" + m.Version),
		})
	}

	for _, t := range g.Tools {
		s.Units = append(s.Units, Unit{
			ID: t.ID, Group: depsGroup, Digest: t.Digest, Kind: kindToolchain,
			Attributes: map[string]any{"version": t.Version},
			Label:      goid.Clip("the toolchain " + strings.TrimPrefix(t.ID, goid.DepNamespace+":") + " " + t.Version),
		})
	}

	for _, l := range g.Links {
		s.Links = append(s.Links, Link{From: l.From, To: l.To, Kind: linkRequires, Narrowing: NarrowModuleGraph, While: plugin.WhileOff})
	}

	for _, c := range g.CannotTell {
		s.CannotTell = append(s.CannotTell, CannotTell{Unit: c.ID, Reason: c.Reason})
	}

	return s, nil
}
