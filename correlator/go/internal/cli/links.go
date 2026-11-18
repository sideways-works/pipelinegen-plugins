package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen-plugins/correlator/go/graph"
	"github.com/sideways-works/pipelinegen-plugins/sbom/go/decls"
	"github.com/sideways-works/pipelinegen/plugin"
)

// setsFile is the file links writes into the work directory, as JSON
// Lines: one package's set per line.
const setsFile = "sets.jsonl"

// links answers the link sets of the packages that hold the asked paths,
// every package's when none is asked. A go.mod asks for every package of its
// module; a directory with no Go code left answers an empty set.
func links(ctx context.Context, req Request, stderr io.Writer) (any, error) {
	if err := needsTree(req); err != nil {
		return nil, err
	}

	if len(req.Config.Modules) == 0 && req.Claimed != nil {
		req.Config.Modules = claimedModules(req.Claimed)
	}

	asked, readers, err := withReaders(req)
	if err != nil {
		return nil, err
	}

	var (
		sets   []plugin.Set
		ans    = plugin.Answer{Files: []string{setsFile}}
		loaded *graph.Tree
	)

	load := asked == nil || slices.ContainsFunc(asked, func(d string) bool { return hasGo(req.Tree, d) })
	if load {
		var dirs []string

		for _, d := range asked {
			if hasGo(req.Tree, d) {
				dirs = append(dirs, d)
			}
		}

		if asked == nil {
			dirs = nil
		}

		t, err := loadPackages(ctx, req, dirs)
		if err != nil {
			return nil, err
		}

		loaded = t

		sets = answeredSets(t, dirs, readers, req.Paths)

		if why := otherGo(ctx, t, req.Config.Build); why != "" {
			say(stderr, "go-correlator: "+why+"; this answer is not cached")

			no := false
			ans.Cacheable = &no
		}
	}

	sets, err = withNames(ctx, req, stderr, sets, &ans, loaded)
	if err != nil {
		return nil, err
	}

	// a directory asked about that answered no set is a package gone
	for _, d := range asked {
		if !readers[d] && !slices.ContainsFunc(sets, func(s plugin.Set) bool { return s.Group == d }) {
			sets = append(sets, plugin.Set{Group: d, Inputs: []string{}})
		}
	}

	slices.SortFunc(sets, func(a, b plugin.Set) int { return strings.Compare(a.Group, b.Group) })

	rows := make([]any, len(sets))
	for i, s := range sets {
		rows[i] = s
	}

	if err := writeLines(filepath.Join(req.Work, setsFile), rows); err != nil {
		return nil, err
	}

	return ans, nil
}

// withNames adds the set of every link that depends on N, which holds every
// Go file: whichever path is asked about, it is answered again.
func withNames(ctx context.Context, req Request, stderr io.Writer, sets []plugin.Set, ans *plugin.Answer, t *graph.Tree) ([]plugin.Set, error) {
	names, err := readNames(ctx, req, stderr)
	if err != nil {
		return nil, err
	}

	if t != nil {
		t.SetNames(names)
		sayForced(stderr, t)
	}

	if names.Uncacheable != "" {
		no := false
		ans.Cacheable = &no
	}

	return append(sets, setOf(names.Set())), nil
}

func needsTree(req Request) error {
	if req.Work == "" {
		return fmt.Errorf("%w: links needs a work directory", errRequest)
	}

	if req.Tree == "" {
		return fmt.Errorf("%w: no tree", errRequest)
	}

	return nil
}

func setOf(s graph.LinkSet) plugin.Set {
	out := plugin.Set{Group: s.Group, Inputs: nonNil(s.Inputs), InputUnits: s.InputUnits, Links: make([]plugin.Link, 0, len(s.Links))}

	for _, c := range s.CannotTell {
		out.CannotTell = append(out.CannotTell, plugin.CannotTell{Unit: c.Unit, Reason: c.Reason})
	}

	for _, l := range s.Links {
		out.Links = append(out.Links, plugin.Link{
			From: l.From, To: l.To, Kind: l.Kind, Narrowing: l.Narrowing, While: l.While, IfLinked: l.IfLinked, Ref: l.Ref,
		})
	}

	return out
}

// askedDirs are the package directories the asked paths name, sorted, or nil
// for the whole tree. A go.mod or go.work names every package of the modules
// below it.
func askedDirs(tree string, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}

	out := []string{}

	for _, p := range paths {
		p = path.Clean(filepath.ToSlash(p))

		switch path.Base(p) {
		case goSum:
			// the go command checks it; no link reads it
			continue
		case goMod:
			dirs, err := decls.Dirs(tree, path.Dir(p))
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("%w: %w", errRequest, err)
			}

			out = append(out, dirs...)

			continue
		case goWork:
			dirs, err := workDirs(tree, path.Dir(p))
			if err != nil {
				return nil, err
			}

			out = append(out, dirs...)

			continue
		}

		if d := path.Dir(p); !skipped(d) {
			out = append(out, d)
		}
	}

	slices.Sort(out)

	return slices.Compact(out), nil
}

func workDirs(tree, dir string) ([]string, error) {
	mods, err := decls.Modules(tree)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRequest, err)
	}

	var out []string

	for _, m := range mods {
		if dir != "." && m != dir && !strings.HasPrefix(m, dir+"/") {
			continue
		}

		dirs, err := decls.Dirs(tree, m)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errRequest, err)
		}

		out = append(out, dirs...)
	}

	return out, nil
}

func skipped(dir string) bool {
	return dir != "." && slices.ContainsFunc(strings.Split(dir, "/"), decls.Skip)
}

func hasGo(tree, dir string) bool {
	return decls.HasGo(filepath.Join(tree, filepath.FromSlash(dir)))
}

// withReaders are the asked directories with the packages whose purity
// proofs read an asked file (their sets name it among their inputs, so the
// core takes their sets back when it asks about the file), and those
// readers that hold no asked path themselves.
func withReaders(req Request) ([]string, map[string]bool, error) {
	asked, err := askedDirs(req.Tree, req.Paths)
	if err != nil || asked == nil {
		return asked, nil, err
	}

	found, err := graph.Readers(req.Tree, graph.Options{
		Modules: req.Config.Modules, PureCalls: req.Config.PureCalls, Build: req.Config.Build,
	}, req.Paths)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", errCannotTell, err)
	}

	readers := map[string]bool{}

	for _, d := range found {
		if !slices.Contains(asked, d) {
			readers[d] = true
			asked = append(asked, d)
		}
	}

	slices.Sort(asked)

	return asked, readers, nil
}

// answeredSets are the sets of the loaded directories, less those of the
// readers the syntax over-approximated: a reader's set that names no asked
// file is served by the cache as it is.
func answeredSets(t *graph.Tree, dirs []string, readers map[string]bool, paths []string) []plugin.Set {
	var out []plugin.Set

	for _, s := range t.LinkSets(dirs) {
		if readers[s.Group] && !slices.ContainsFunc(s.Inputs, func(in string) bool { return slices.Contains(paths, in) }) {
			continue
		}

		out = append(out, setOf(s))
	}

	return out
}
