// Package proof measures a narrowing on a real change: it walks each recorded
// slice through the link sets of both trees, with the narrowing on and off,
// the way pipelinegen's planner does, and compares the walk with the plan the
// release before made of the same change.
package proof

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen-plugins/correlator/go/graph"
	"github.com/sideways-works/pipelinegen-sbom-go/decls"
	"github.com/sideways-works/pipelinegen-sbom-go/goid"
)

// errOutside is an archive entry that would land outside the tree.
var errOutside = errors.New("git archive: an entry leaves the tree")

type Options struct {
	// Repo is the git repository holding both commits; it is only read.
	Repo       string
	Base, Head string
	// Lists is the directory of the recorded lists, <suite>/<slice>.txt with a
	// unit id per line; a slice named _setup is left out.
	Lists string
	// Plan is the plan the release before made of the same change, as
	// `pipelinegen affected --json` writes it.
	Plan string
	// Narrowing is the narrowing measured, interface-methods by default.
	Narrowing string
	// Modules are the module directories to read; none reads every one.
	Modules []string
	// Cache keeps what dependencies' sources hold between runs.
	Cache string
	// Work is where the two trees are written; it must exist.
	Work string
	// Explain are nodes whose links, in force with the narrowing on, to
	// list where the trees differ.
	Explain []string
	// AnyLink counts a node changed by any link only one tree has. By default
	// only a link the executed width follows, to a target that is no changed
	// unit, counts: a changed target is a change of its own.
	AnyLink bool
}

type Report struct {
	Narrowing string        `json:"narrowing"`
	Level     int           `json:"level"`
	Suites    []SuiteReport `json:"suites"`
	Slices    []SliceResult `json:"slices"`
	// CannotTell are the units a correlator couldn't tell about, in
	// either tree: the planner binds what reaches them wide.
	CannotTell []string `json:"cannot_tell,omitempty"`
	// Explained are, per node asked about, its links in force with the
	// narrowing on that only the base or only the head has.
	Explained map[string][2][]string `json:"explained,omitempty"`
}

type SuiteReport struct {
	Suite string `json:"suite"`
	Total int    `json:"total"`
	// Release, ReleaseGo and Outside are the slices the release selected:
	// all of them, those with a Go reason, those with a reason outside Go
	// (a Bazel target, another plugin's unit, no recording).
	Release   int `json:"release"`
	ReleaseGo int `json:"release_go"`
	Outside   int `json:"outside"`
	// Selected are the slices selected with the narrowing on: Kept for a
	// reason outside Go, ByCode reaching a changed unit other than a whole
	// type, WholeOnly reaching only whole types (WholePaths counts their
	// paths), Extra those the release didn't select.
	Selected   int            `json:"selected"`
	Kept       int            `json:"kept"`
	ByCode     int            `json:"by_code"`
	WholeOnly  int            `json:"whole_only"`
	Extra      int            `json:"extra"`
	WholePaths map[string]int `json:"whole_paths,omitempty"`
	// Off are the slices selected with the narrowing off; OffOnly and
	// ReleaseOnly name those only it, or only the release, selects for a
	// Go reason; UnitsAgree counts the release's Go slices whose changed
	// Go units the walk reaches with the narrowing off.
	Off         int      `json:"off"`
	OffOnly     []string `json:"off_only,omitempty"`
	ReleaseOnly []string `json:"release_only,omitempty"`
	UnitsAgree  int      `json:"units_agree"`
}

// SliceResult is one slice: why the release selected it, and the changed
// units the walk reaches with the narrowing on and off, each with the path
// from the slice's own units (the tree it was found in first).
type SliceResult struct {
	Suite   string              `json:"suite"`
	Slice   string              `json:"slice"`
	Release []string            `json:"release,omitempty"`
	Outside []string            `json:"outside,omitempty"`
	On      map[string][]string `json:"on,omitempty"`
	Off     map[string][]string `json:"off,omitempty"`
}

func Run(ctx context.Context, o Options) (*Report, error) {
	if o.Narrowing == "" {
		o.Narrowing = graph.NarrowInterfaceMethods
	}

	pl, err := readPlan(o.Plan)
	if err != nil {
		return nil, err
	}

	ss, err := readLists(o.Lists)
	if err != nil {
		return nil, err
	}

	var sides [2]*side

	for i, commit := range []string{o.Base, o.Head} {
		dir := filepath.Join(o.Work, []string{"base", "head"}[i])
		if err := export(ctx, o.Repo, commit, dir); err != nil {
			return nil, err
		}

		if sides[i], err = read(ctx, dir, o); err != nil {
			return nil, err
		}
	}

	on := inForce(pl.Narrowing, "")
	off := inForce(pl.Narrowing, o.Narrowing)
	changedOn, changedOff := changed(sides, on, !o.AnyLink), changed(sides, off, !o.AnyLink)

	rep := &Report{Narrowing: o.Narrowing, Level: pl.Narrowing}

	for _, s := range sides {
		rep.CannotTell = append(rep.CannotTell, s.cannot...)
	}

	slices.Sort(rep.CannotTell)
	rep.CannotTell = slices.Compact(rep.CannotTell)

	for _, sl := range ss {
		r := SliceResult{Suite: sl.Suite, Slice: sl.Name, On: map[string][]string{}, Off: map[string][]string{}}
		r.Release, r.Outside = pl.reasons(sl.Suite, sl.Name)

		for i, s := range sides {
			roots := s.roots(sl)
			reach(s, roots, on, changedOn, i, r.On)
			reach(s, roots, off, changedOff, i, r.Off)
		}

		rep.Slices = append(rep.Slices, r)
	}

	rep.Suites = summarise(pl, rep.Slices)

	for _, id := range o.Explain {
		if rep.Explained == nil {
			rep.Explained = map[string][2][]string{}
		}

		rep.Explained[id] = linkDiff(sides, on, id)
	}

	return rep, nil
}

type unit struct{ digest, file string }

type side struct {
	units  map[string]unit
	links  map[string][]graph.Link
	cannot []string
}

func read(ctx context.Context, root string, o Options) (*side, error) {
	s := &side{units: map[string]unit{}, links: map[string][]graph.Link{}}

	mods := o.Modules
	if len(mods) == 0 {
		mods = []string{"."}
	}

	for _, m := range mods {
		dirs, err := decls.Dirs(root, m)
		if err != nil {
			return nil, fmt.Errorf("units of %s: %w", m, err)
		}

		for _, d := range dirs {
			p, err := decls.Read(root, d)
			if err != nil {
				// a package go-sbom can't read is no unit of either tree
				continue
			}

			for _, u := range p.Units {
				s.units[string(u.ID)] = unit{u.Digest, u.File}
			}
		}
	}

	t, err := graph.Load(ctx, root, graph.Options{Modules: o.Modules})
	if err != nil {
		return nil, fmt.Errorf("links: %w", err)
	}

	n, err := graph.ReadNames(ctx, root, graph.NamesOptions{Modules: o.Modules, Cache: o.Cache})
	if err != nil {
		return nil, fmt.Errorf("method names: %w", err)
	}

	t.SetNames(n)

	for _, ls := range append(t.LinkSets(nil), n.Set()) {
		for _, l := range ls.Links {
			s.links[l.From] = append(s.links[l.From], l)
		}

		for _, c := range ls.CannotTell {
			s.cannot = append(s.cannot, c.Unit+": "+c.Reason)
		}
	}

	return s, nil
}

// roots are a slice's own units in a tree: its list, and the declarations
// of its own file (a test type stands for itself by its declaration).
func (s *side) roots(sl recorded) []string {
	roots := slices.Clone(sl.List)

	for id, u := range s.units {
		if u.file != sl.Name {
			continue
		}

		if dir, kind, name, err := goid.Parse(id); err == nil && kind == goid.Type {
			if _, twin := s.units[string(goid.New(dir, goid.TypeDecl, name))]; twin {
				continue
			}
		}

		roots = append(roots, id)
	}

	slices.Sort(roots)

	return roots
}

func inForce(level int, less string) map[string]bool {
	on := map[string]bool{}

	for _, n := range graph.Narrowings {
		if n.OffAt > level && n.Name != less {
			on[n.Name] = true
		}
	}

	return on
}

func holds(l graph.Link, on map[string]bool) bool {
	switch {
	case l.Narrowing == "":
		return true
	case l.While == graph.WhileOn:
		return on[l.Narrowing]
	}

	return !on[l.Narrowing]
}

// changed are the nodes that differ between the trees, as the planner
// counts them: a unit whose digest differs or that one tree lacks, and a
// node one of whose links in force only one tree has; with followed, only
// a link the executed width follows to a target that is no changed unit.
func changed(sides [2]*side, on map[string]bool, followed bool) map[string]bool {
	out := map[string]bool{}

	for _, s := range sides {
		for id := range s.units {
			a, inBase := sides[0].units[id]
			b, inHead := sides[1].units[id]

			if inBase != inHead || a.digest != b.digest {
				out[id] = true
			}
		}
	}

	key := func(l graph.Link) string { return l.To + "\x00" + l.Kind }

	for _, s := range sides {
		for from := range s.links {
			var sets [2][]string

			for i, t := range sides {
				for _, l := range t.links[from] {
					if !holds(l, on) || (followed && !followedSame(sides, l)) {
						continue
					}

					sets[i] = append(sets[i], key(l))
				}

				slices.Sort(sets[i])
				sets[i] = slices.Compact(sets[i])
			}

			if !slices.Equal(sets[0], sets[1]) {
				out[from] = true
			}
		}
	}

	return out
}

// followedSame tells whether the executed width follows a link (any kind
// but dependents-only) and its target is no changed unit: a unit both
// trees hold with the same digest, or a node.
func followedSame(sides [2]*side, l graph.Link) bool {
	if l.Kind == graph.KindDependentsOnly {
		return false
	}

	a, inBase := sides[0].units[l.To]
	b, inHead := sides[1].units[l.To]

	return inBase == inHead && a.digest == b.digest
}

func linkDiff(sides [2]*side, on map[string]bool, id string) [2][]string {
	var sets [2]map[string]bool

	for i, s := range sides {
		sets[i] = map[string]bool{}

		for _, l := range s.links[id] {
			if holds(l, on) {
				sets[i][l.Kind+" "+l.To] = true
			}
		}
	}

	var out [2][]string

	for i := range sets {
		for k := range sets[i] {
			if !sets[1-i][k] {
				out[i] = append(out[i], k)
			}
		}

		slices.Sort(out[i])
	}

	return out
}

// reach adds to dst the changed nodes the executed width reaches from the
// roots in one tree, each with its path, unless an earlier tree found it.
func reach(s *side, roots []string, on, changed map[string]bool, tree int, dst map[string][]string) {
	pred := map[string]string{}
	expanded := map[string]bool{}
	queue := slices.Clone(roots)

	for _, r := range roots {
		pred[r] = ""
	}

	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]

		if expanded[n] {
			continue
		}

		expanded[n] = true

		for _, l := range s.links[n] {
			if !holds(l, on) || (l.Kind != graph.KindFollow && l.Kind != graph.KindBind) {
				continue
			}

			if _, ok := pred[l.To]; !ok {
				pred[l.To] = n
			}

			if l.Kind == graph.KindFollow {
				queue = append(queue, l.To)
			}
		}
	}

	for id := range pred {
		if !changed[id] {
			continue
		}

		if _, ok := dst[id]; ok {
			continue
		}

		path := []string{[]string{"base", "head"}[tree]}
		for at := id; at != "" && len(path) < 16; at = pred[at] {
			path = append(path, at)
		}

		dst[id] = path
	}
}

type recorded struct {
	Suite, Name string
	List        []string
}

func readLists(dir string) ([]recorded, error) {
	suites, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("lists: %w", err)
	}

	var out []recorded

	for _, e := range suites {
		if !e.IsDir() || strings.HasPrefix(e.Name(), "_") {
			continue
		}

		root := filepath.Join(dir, e.Name())

		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".txt") {
				return err
			}

			name := filepath.ToSlash(strings.TrimSuffix(strings.TrimPrefix(p, root+string(filepath.Separator)), ".txt"))
			if name == "_setup" {
				return nil
			}

			b, err := os.ReadFile(p) //nolint:gosec // a list of the export the caller names
			if err != nil {
				return fmt.Errorf("list %s: %w", p, err)
			}

			sl := recorded{Suite: e.Name(), Name: name}

			for l := range strings.SplitSeq(string(b), "\n") {
				if strings.HasPrefix(l, "go:") {
					sl.List = append(sl.List, l)
				}
			}

			out = append(out, sl)

			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("lists: %w", err)
		}
	}

	slices.SortFunc(out, func(a, b recorded) int {
		return strings.Compare(a.Suite+"\x00"+a.Name, b.Suite+"\x00"+b.Name)
	})

	return out, nil
}

type plan struct {
	Narrowing int `json:"narrowing"`
	Suites    []struct {
		Suite    string `json:"suite"`
		Selected []struct {
			Name  string `json:"name"`
			Label string `json:"label"`
		} `json:"selected"`
		Untouched []string `json:"untouched"`
		Reasons   map[string][]struct {
			Kind string `json:"kind"`
			What string `json:"what"`
		} `json:"reasons"`
	} `json:"suites"`
}

func readPlan(path string) (*plan, error) {
	b, err := os.ReadFile(path) //nolint:gosec // the plan the caller names
	if err != nil {
		return nil, fmt.Errorf("plan: %w", err)
	}

	var p plan
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("plan %s: %w", path, err)
	}

	return &p, nil
}

// goReason tells whether a reason of the plan comes from the Go plugins. A
// slice without a recording, a Bazel target or another plugin's unit is a
// reason outside Go.
func goReason(kind, what string) bool {
	if kind == "everything" {
		return !strings.HasPrefix(what, "it has no recording")
	}

	for _, p := range []string{"go:", "dep:go:", "group:", "node:go:"} {
		if strings.HasPrefix(what, p) {
			return true
		}
	}

	return false
}

func (p *plan) reasons(suite, name string) ([]string, []string) {
	var goIDs, outside []string

	for _, s := range p.Suites {
		if s.Suite != suite {
			continue
		}

		for _, sel := range s.Selected {
			if sel.Name != name {
				continue
			}

			for _, r := range s.Reasons[sel.Label] {
				if goReason(r.Kind, r.What) {
					goIDs = append(goIDs, r.What)
				} else {
					outside = append(outside, r.Kind+": "+r.What)
				}
			}

			if len(goIDs) == 0 && len(outside) == 0 {
				outside = append(outside, "selected without a reason the plan names")
			}
		}
	}

	slices.Sort(goIDs)
	slices.Sort(outside)

	return slices.Compact(goIDs), slices.Compact(outside)
}

func summarise(p *plan, rs []SliceResult) []SuiteReport {
	by := map[string][]SliceResult{}
	for _, r := range rs {
		by[r.Suite] = append(by[r.Suite], r)
	}

	var out []SuiteReport

	for _, s := range p.Suites {
		results, ok := by[s.Suite]
		if !ok {
			continue
		}

		sr := SuiteReport{Suite: s.Suite, Total: len(s.Selected) + len(s.Untouched), Release: len(s.Selected), WholePaths: map[string]int{}}

		var selected []string
		for _, sel := range s.Selected {
			selected = append(selected, sel.Name)
		}

		releaseGo := sr.released(selected, results)
		sr.walked(results, releaseGo)

		out = append(out, sr)
	}

	return out
}

func (sr *SuiteReport) released(selected []string, results []SliceResult) map[string]bool {
	byName := map[string]SliceResult{}
	for _, r := range results {
		byName[r.Slice] = r
	}

	releaseGo := map[string]bool{}

	for _, name := range selected {
		r, isRecorded := byName[name]

		switch {
		case !isRecorded, len(r.Outside) > 0:
			sr.Outside++
			sr.Kept++
			sr.Selected++
		case len(r.On) > 0:
			sr.Selected++
			sr.classify(r)
		}

		if isRecorded && len(r.Release) > 0 {
			releaseGo[name] = true
		}
	}

	sr.ReleaseGo = len(releaseGo)

	return releaseGo
}

func (sr *SuiteReport) walked(results []SliceResult, releaseGo map[string]bool) {
	for _, r := range results {
		if len(r.On) > 0 && len(r.Release) == 0 && len(r.Outside) == 0 {
			sr.Selected++
			sr.Extra++
		}

		switch {
		case len(r.Off) > 0:
			sr.Off++

			if !releaseGo[r.Slice] {
				sr.OffOnly = append(sr.OffOnly, r.Slice)
			}
		case releaseGo[r.Slice]:
			sr.ReleaseOnly = append(sr.ReleaseOnly, r.Slice)
		}

		if releaseGo[r.Slice] && reachesAll(r.Off, r.Release) {
			sr.UnitsAgree++
		}
	}
}

func (sr *SuiteReport) classify(r SliceResult) {
	ids := slices.Sorted(func(yield func(string) bool) {
		for id := range r.On {
			if !yield(id) {
				return
			}
		}
	})

	for _, id := range ids {
		if _, kind, _, err := goid.Parse(id); err != nil || kind != goid.Type {
			sr.ByCode++

			return
		}
	}

	sr.WholeOnly++

	path := r.On[ids[0]]
	if len(path) > 5 {
		path = path[len(path)-4:]
	}

	sr.WholePaths[strings.Join(path, " < ")]++
}

// reachesAll tells whether the walk reached every Go unit the release
// named, the units only one go-sbom lists aside.
func reachesAll(reached map[string][]string, named []string) bool {
	for _, id := range named {
		if strings.Contains(id, "#typedecl:") || strings.Contains(id, "#methodsig:") {
			continue
		}

		if _, ok := reached[id]; !ok {
			return false
		}
	}

	return true
}

// export writes a commit's tree into dir with git archive; the repository
// is only read.
func export(ctx context.Context, repo, commit, dir string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "archive", "--format=tar", commit) //nolint:gosec // git, on the caller's repository and commit

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("git archive %s: %w", commit, err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("git archive %s: %w", commit, err)
	}

	untarErr := untar(out, dir)

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("git archive %s: %w: %s", commit, err, strings.TrimSpace(stderr.String()))
	}

	return untarErr
}

func untar(r io.Reader, dir string) error {
	tr := tar.NewReader(r)

	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("git archive: %w", err)
		}

		name := filepath.Clean(filepath.FromSlash(h.Name))
		if !filepath.IsLocal(name) {
			return fmt.Errorf("%w: %s", errOutside, h.Name)
		}

		if err := entry(tr, h, filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("git archive: %w", err)
		}
	}
}

func entry(r io.Reader, h *tar.Header, p string) error {
	wrap := func(err error) error {
		if err != nil {
			return fmt.Errorf("%s: %w", h.Name, err)
		}

		return nil
	}

	if h.Typeflag == tar.TypeDir {
		return wrap(os.MkdirAll(p, 0o750))
	}

	if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeSymlink {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return wrap(err)
	}

	if h.Typeflag == tar.TypeSymlink {
		return wrap(os.Symlink(h.Linkname, p))
	}

	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, h.FileInfo().Mode().Perm()|0o600) //nolint:gosec // a file of the archive, inside the work directory
	if err != nil {
		return wrap(err)
	}

	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close() //nolint:errcheck // the copy's error is the one to report

		return wrap(err)
	}

	return wrap(f.Close())
}

func (r *Report) Write(w io.Writer) {
	say := func(format string, a ...any) {
		if _, err := fmt.Fprintf(w, format+"\n", a...); err != nil {
			return
		}
	}

	say("%s, measured at narrowing level %d", r.Narrowing, r.Level)

	for _, s := range r.Suites {
		say("%s: %d slices; the release selected %d (%d for a Go reason, %d for a reason outside Go)",
			s.Suite, s.Total, s.Release, s.ReleaseGo, s.Outside)
		say("  with %s: %d selected: %d kept for a reason outside Go, %d by changed code, %d only through a whole type, %d the release left out",
			r.Narrowing, s.Selected, s.Kept, s.ByCode, s.WholeOnly, s.Extra)

		for _, p := range sortedByCount(s.WholePaths) {
			say("    %d through %s", s.WholePaths[p], p)
		}

		same := "the same slices"
		if len(s.OffOnly)+len(s.ReleaseOnly) > 0 {
			same = fmt.Sprintf("%d only the walk selects (%s), %d only the release", len(s.OffOnly), strings.Join(first(s.OffOnly, 5), ", "),
				len(s.ReleaseOnly))
		}

		say("  with %s off: %d selected, against the release's %d for a Go reason: %s; the release's Go units reached in %d of %d",
			r.Narrowing, s.Off, s.ReleaseGo, same, s.UnitsAgree, s.ReleaseGo)
	}

	for _, id := range slices.Sorted(maps.Keys(r.Explained)) {
		d := r.Explained[id]
		say("%s: only the base links %s; only the head links %s", id, orNone(d[0]), orNone(d[1]))
	}

	if len(r.CannotTell) > 0 {
		say("units a correlator couldn't tell about: %d (%s)", len(r.CannotTell), strings.Join(first(r.CannotTell, 5), ", "))
	}
}

func orNone(xs []string) string {
	if len(xs) == 0 {
		return "nothing"
	}

	return strings.Join(xs, ", ")
}

func first(xs []string, n int) []string {
	if len(xs) > n {
		return append(slices.Clone(xs[:n]), "...")
	}

	return xs
}

func sortedByCount(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	slices.SortFunc(keys, func(a, b string) int {
		if m[a] != m[b] {
			return m[b] - m[a]
		}

		return strings.Compare(a, b)
	})

	return keys
}
