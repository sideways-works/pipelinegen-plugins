package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen-plugins/correlator/go/graph"
	"github.com/sideways-works/pipelinegen-plugins/sbom/go/decls"
	"github.com/sideways-works/pipelinegen/plugin"
)

// The files bind writes into the work directory, as JSON Lines.
const (
	unitsFile   = "units.jsonl"
	bindFile    = "bind.jsonl"
	unboundFile = "unbound.jsonl"
)

// bindLine is one slice's binding in one width. Units are indexes into
// units.jsonl, each with its reason's kind and the index it came through
// (-1 for none).
type bindLine struct {
	Suite      string   `json:"suite"`
	Slice      string   `json:"slice"`
	Width      string   `json:"width"`
	Everything bool     `json:"everything,omitempty"`
	Whole      []string `json:"whole_groups"`
	Linked     []string `json:"linked"`
	// Units are [index, reason, via index], and for a type, constant or
	// variable a fourth element: every bound referrer's index.
	Units   [][]any          `json:"units"`
	Dropped map[string][]int `json:"dropped,omitempty"`
	// DroppedGroups are, per narrowing, the groups whose unbound names the
	// wide rule would bind.
	DroppedGroups map[string][]string `json:"dropped_groups,omitempty"`
}

type unboundLine struct {
	Group string   `json:"group"`
	Width string   `json:"width"`
	Names []string `json:"names"`
	// Narrowed are, per narrowing in force, the declarations that
	// narrowing alone counts as reached: under its wide rule the unbound
	// names are Names and these.
	Narrowed map[string][]string `json:"narrowed,omitempty"`
}

type table struct {
	ids   []string
	index map[string]int
}

func (t *table) at(id string) int {
	if id == "" {
		return -1
	}

	if i, ok := t.index[id]; ok {
		return i
	}

	t.index[id] = len(t.ids)
	t.ids = append(t.ids, id)

	return len(t.ids) - 1
}

func bind(ctx context.Context, req Request, stderr io.Writer) (any, error) {
	if req.Work == "" {
		return nil, fmt.Errorf("%w: bind needs a work directory", errRequest)
	}

	if len(req.Config.Modules) == 0 && req.Claimed != nil {
		req.Config.Modules = claimedModules(req.Claimed)
	}

	t, err := load(ctx, req)
	if err != nil {
		return nil, err
	}

	names, err := readNames(ctx, req, stderr)
	if err != nil {
		return nil, err
	}

	t.SetNames(names)
	sayForced(stderr, t)

	ss := make([]graph.Slice, 0, len(req.Slices))

	for _, s := range req.Slices {
		list := s.List
		for _, r := range req.Roots[s.Name] {
			if strings.HasPrefix(r, "go:") {
				list = append(slices.Clone(list), r)
			}
		}

		ss = append(ss, graph.Slice{
			Suite: s.Suite, Name: s.Name, Sources: s.Sources, Tests: s.Tests,
			List: list, HasList: s.List != nil, Depth: s.Depth,
		})
	}

	a := t.Bind(graph.Request{Slices: ss, Narrowings: req.Narrowings, Setup: req.Setup})

	ans, err := writeAnswer(req.Work, a, widths(req.Widths))
	if err != nil {
		return nil, err
	}

	if why := otherGo(ctx, t, req.Config.Build); why != "" || names.Uncacheable != "" {
		if why != "" {
			say(stderr, "go-correlator: "+why+"; this answer is not cached")
		}

		no := false
		ans.Cacheable = &no
	}

	return ans, nil
}

// otherGo says why the go that loaded the tree may not be the go describe
// reported, which the core keys a cached answer by: a shim can pick a
// different go in each module's directory.
func otherGo(ctx context.Context, t *graph.Tree, b decls.Build) string {
	if t.Uncacheable != "" {
		// a module the go command is too old for is bound wide: another
		// machine's go may read it
		return t.Uncacheable
	}

	here, err := graph.GoVersion(ctx, "", b)
	if err != nil {
		return err.Error()
	}

	for _, dir := range sortedKeys(t.GoVersions) {
		if v := t.GoVersions[dir]; v != here {
			return fmt.Sprintf("the go command in module %s is %s, in the working directory %s", dir, v, here)
		}
	}

	return ""
}

func widths(names []string) []graph.Width {
	if len(names) == 0 {
		return graph.Widths
	}

	var out []graph.Width

	for _, w := range graph.Widths {
		if slices.Contains(names, w.String()) {
			out = append(out, w)
		}
	}

	return out
}

func writeAnswer(work string, a graph.Answer, ws []graph.Width) (plugin.Answer, error) {
	tab := &table{index: map[string]int{}}
	lines := make([]any, 0, len(a.Results))

	for _, r := range a.Results {
		if !slices.Contains(ws, r.Width) {
			continue
		}

		lines = append(lines, bindLineOf(tab, r))
	}

	var unbound []any

	for _, w := range ws {
		for _, g := range sortedKeys(a.Unbound[w]) {
			l := unboundLine{Group: g, Width: w.String(), Names: a.Unbound[w][g]}

			for _, n := range sortedKeys(a.Narrowed) {
				if ids := a.Narrowed[n][w][g]; len(ids) > 0 {
					if l.Narrowed == nil {
						l.Narrowed = map[string][]string{}
					}

					l.Narrowed[n] = ids
				}
			}

			unbound = append(unbound, l)
		}
	}

	ids := make([]any, len(tab.ids))
	for i, id := range tab.ids {
		ids[i] = id
	}

	for name, rows := range map[string][]any{unitsFile: ids, bindFile: lines, unboundFile: unbound} {
		if err := writeLines(filepath.Join(work, name), rows); err != nil {
			return plugin.Answer{}, err
		}
	}

	return plugin.Answer{Files: []string{unitsFile, bindFile, unboundFile}}, nil
}

func bindLineOf(tab *table, r graph.Result) bindLine {
	b := r.Binding
	l := bindLine{
		Suite: r.Suite, Slice: r.Slice, Width: r.Width.String(), Everything: b.Everything,
		Whole: nonNil(b.Whole), Linked: nonNil(b.Linked), Units: [][]any{},
	}

	for _, id := range sortedKeys(b.Units) {
		why := b.Units[id]

		via := -1
		if strings.Contains(why.Via, ":") {
			via = tab.at(why.Via)
		}

		u := []any{tab.at(id), why.Kind, via}

		if vs, ok := b.Vias[id]; ok {
			all := make([]int, 0, len(vs))
			for _, v := range vs {
				all = append(all, tab.at(v))
			}

			u = append(u, all)
		}

		l.Units = append(l.Units, u)
	}

	for _, n := range sortedKeys(b.Dropped) {
		for _, id := range b.Dropped[n] {
			if g, ok := strings.CutPrefix(id, "unbound:"); ok {
				if l.DroppedGroups == nil {
					l.DroppedGroups = map[string][]string{}
				}

				l.DroppedGroups[n] = append(l.DroppedGroups[n], g)

				continue
			}

			if l.Dropped == nil {
				l.Dropped = map[string][]int{}
			}

			l.Dropped[n] = append(l.Dropped[n], tab.at(id))
		}
	}

	return l
}

func writeLines(path string, rows []any) error {
	f, err := os.Create(path) //nolint:gosec // a file of the call's work directory
	if err != nil {
		return fmt.Errorf("bind: %w", err)
	}

	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)

	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			_ = f.Close() //nolint:errcheck // the encode error is the one to report

			return fmt.Errorf("bind: %w", err)
		}
	}

	if err := w.Flush(); err != nil {
		_ = f.Close() //nolint:errcheck // as above

		return fmt.Errorf("bind: %w", err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("bind: %w", err)
	}

	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	slices.Sort(out)

	return out
}
