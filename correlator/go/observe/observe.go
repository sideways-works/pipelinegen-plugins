// Package observe turns the coverage a slice left behind into the
// declarations it ran: each top-level function, method or init holding a
// covered block, named by goid as go-sbom lists it.
package observe

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/sideways-works/pipelinegen-plugins/sbom/go/decls"
	"github.com/sideways-works/pipelinegen-plugins/sbom/go/goid"
)

// ErrMissing is coverage that isn't there: a directory without a
// GOCOVERDIR's files or a coverprofile. The protocol answers it with
// "can't tell", never with an empty list.
var ErrMissing = errors.New("no coverage")

// Observation is what a slice touched, and the part of it that ran as
// loading: what only its batch's setup ran, and package initialisers.
type Observation struct {
	Touched   []string
	SetupLike []string
}

// Observe reads the coverage in dirs: the first is the batch's setup when
// there are more, the rest the slice's own parts. A part without coverage is
// ErrMissing. Blocks of other modules are left out; blocks of the tree's
// packages that map to no declaration, or no block of the tree at all, are
// ErrSource.
func Observe(ctx context.Context, root, gocmd string, dirs []string, modules ...string) (Observation, error) {
	if len(dirs) == 0 {
		return Observation{}, fmt.Errorf("%w: no coverage directory", ErrMissing)
	}

	m, err := newMapper(root, modules)
	if err != nil {
		return Observation{}, err
	}

	var own, setup []block

	parts := dirs
	if len(dirs) > 1 {
		parts = dirs[1:]

		bs, err := blocksIn(ctx, gocmd, dirs[0])
		if err != nil && !errors.Is(err, ErrMissing) {
			return Observation{}, fmt.Errorf("setup: %w", err)
		}

		setup = bs
	}

	for _, dir := range parts {
		bs, err := blocksIn(ctx, gocmd, dir)
		if err != nil {
			return Observation{}, err
		}

		own = append(own, bs...)
	}

	if !slices.ContainsFunc(own, m.inTree) {
		return Observation{}, m.foreign(own)
	}

	ownIDs, err := m.ids(own)
	if err != nil {
		return Observation{}, err
	}

	setupIDs, err := m.ids(setup)
	if err != nil {
		return Observation{}, fmt.Errorf("setup: %w", err)
	}

	// what the setup ran belongs to the slice too: with the setup split
	// off it binds the slice like the rest
	touched := append(slices.Clone(ownIDs), setupIDs...)
	slices.Sort(touched)
	touched = slices.Compact(touched)

	var like []string

	for _, id := range touched {
		_, own := slices.BinarySearch(ownIDs, id)
		if !own || goid.ID(id).Kind() == goid.Init {
			like = append(like, id)
		}
	}

	return Observation{Touched: touched, SetupLike: like}, nil
}

func blocksIn(ctx context.Context, gocmd, dir string) ([]block, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMissing, err)
	}

	profiles, err := filepath.Glob(filepath.Join(dir, "*.coverprofile"))
	if err != nil {
		return nil, fmt.Errorf("coverage: %w", err)
	}

	var texts [][]byte

	for _, p := range profiles {
		b, err := os.ReadFile(p) //nolint:gosec // the slice's own coverage output
		if err != nil {
			return nil, fmt.Errorf("coverage: %w", err)
		}

		texts = append(texts, b)
	}

	for _, covdir := range []string{filepath.Join(dir, "gocoverdir"), dir} {
		metas, err := filepath.Glob(filepath.Join(covdir, "covmeta.*"))
		if err != nil {
			return nil, fmt.Errorf("coverage: %w", err)
		}

		if len(metas) > 0 {
			b, err := textfmt(ctx, gocmd, covdir)
			if err != nil {
				return nil, err
			}

			texts = append(texts, b)
		}
	}

	if len(texts) == 0 {
		return nil, fmt.Errorf("%w: no gocoverdir/, covmeta file or *.coverprofile in %s", ErrMissing, dir)
	}

	return parseProfiles(texts)
}

func textfmt(ctx context.Context, gocmd, dir string) ([]byte, error) {
	if gocmd == "" {
		gocmd = "go"
	}

	tmp, err := os.MkdirTemp("", "go-correlator-covdata-")
	if err != nil {
		return nil, fmt.Errorf("coverage: %w", err)
	}
	defer os.RemoveAll(tmp) //nolint:errcheck // a temporary directory; a leftover harms nothing

	out := filepath.Join(tmp, "profile")

	cmd := exec.CommandContext(ctx, gocmd, "tool", "covdata", "textfmt", "-i", dir, "-o", out) //nolint:gosec // the configured go command
	if b, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("coverage: go tool covdata: %w: %s", err, b)
	}

	b, err := os.ReadFile(out) //nolint:gosec // written just above
	if err != nil {
		return nil, fmt.Errorf("coverage: %w", err)
	}

	return b, nil
}

type block struct {
	file      string
	line, col int
	ran       bool
}

var ErrProfile = errors.New("coverprofile")

func parseProfiles(texts [][]byte) ([]block, error) {
	seen := map[block]bool{}

	for _, text := range texts {
		sc := bufio.NewScanner(bytes.NewReader(text))
		sc.Buffer(make([]byte, 1<<20), 1<<20)

		for sc.Scan() {
			line := sc.Text()
			if line == "" || strings.HasPrefix(line, "mode:") {
				continue
			}

			b, count, err := parseLine(line)
			if err != nil {
				return nil, err
			}

			seen[b] = seen[b] || count > 0
		}

		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrProfile, err)
		}
	}

	out := make([]block, 0, len(seen))
	for b, ran := range seen {
		b.ran = ran
		out = append(out, b)
	}

	return out, nil
}

// parseLine reads `file:l.c,l.c statements count`.
func parseLine(line string) (block, int, error) {
	bad := func() (block, int, error) { return block{}, 0, fmt.Errorf("%w: bad line %q", ErrProfile, line) }

	file, rest, ok := strings.CutLast(line, ":")
	if !ok {
		return bad()
	}

	f := strings.Fields(rest)
	if len(f) != 3 {
		return bad()
	}

	start, _, ok := strings.Cut(f[0], ",")
	if !ok {
		return bad()
	}

	ls, cs, ok := strings.Cut(start, ".")
	if !ok {
		return bad()
	}

	l, err1 := strconv.Atoi(ls)
	c, err2 := strconv.Atoi(cs)
	count, err3 := strconv.Atoi(f[2])

	if err1 != nil || err2 != nil || err3 != nil {
		return bad()
	}

	return block{file: file, line: l, col: c}, count, nil
}

type mapper struct {
	root  string
	mods  []mod
	fset  *token.FileSet
	files map[string]*goid.Index
}

type mod struct{ dir, path string }

// newMapper reads the tree's modules: those named, else every go.mod of the
// tree. A module the config leaves out is no module of the tree.
func newMapper(root string, modules []string) (*mapper, error) {
	root, err := decls.Root(root)
	if err != nil {
		return nil, fmt.Errorf("coverage: %w", err)
	}

	dirs := modules
	if len(dirs) == 0 {
		dirs, err = decls.Modules(root)
		if err != nil {
			return nil, fmt.Errorf("coverage: %w", err)
		}
	}

	if len(dirs) == 0 {
		return nil, fmt.Errorf("%w: no go.mod under %s to map coverage to", ErrSource, root)
	}

	m := &mapper{root: root, fset: token.NewFileSet(), files: map[string]*goid.Index{}}

	for _, d := range dirs {
		b, err := os.ReadFile(filepath.Join(root, d, "go.mod")) //nolint:gosec // a go.mod of the tree
		if err != nil {
			return nil, fmt.Errorf("coverage: %w", err)
		}

		if p := modfile.ModulePath(b); p != "" {
			m.mods = append(m.mods, mod{dir: d, path: p})
		}
	}

	return m, nil
}

func (m *mapper) dirOf(importPath string) (string, bool) {
	best := -1

	for i, md := range m.mods {
		if (importPath == md.path || strings.HasPrefix(importPath, md.path+"/")) &&
			(best < 0 || len(md.path) > len(m.mods[best].path)) {
			best = i
		}
	}

	if best < 0 {
		return "", false
	}

	md := m.mods[best]

	return path.Clean(path.Join(md.dir, strings.TrimPrefix(importPath, md.path))), true
}

// ErrSource is coverage that can't be attributed to the tree. An answer
// without those blocks would drop what they ran, so it is "can't tell".
var ErrSource = errors.New("coverage doesn't match the tree")

func (m *mapper) foreign(blocks []block) error {
	var pkgs []string

	mods := make([]string, 0, len(m.mods))
	ran := 0

	for _, b := range blocks {
		if b.ran {
			ran++
		}

		if p := path.Dir(b.file); !slices.Contains(pkgs, p) {
			pkgs = append(pkgs, p)
		}
	}

	for _, md := range m.mods {
		mods = append(mods, md.path)
	}

	slices.Sort(pkgs)

	if len(pkgs) > 3 {
		pkgs = append(pkgs[:3], "...")
	}

	return fmt.Errorf("%w: the slice's coverage (%s, %s) holds none of the tree's modules (%s): "+
		"a nested module the config doesn't list, or coverage of a build from another checkout or module path?",
		ErrSource, count(ran, "covered block"), strings.Join(pkgs, ", "), strings.Join(mods, ", "))
}

func (m *mapper) inTree(b block) bool {
	_, ok := m.dirOf(path.Dir(b.file))

	return ok
}

type unattributed struct {
	blocks int
	files  []string
}

func (u *unattributed) add(file string) {
	u.blocks++

	if !slices.Contains(u.files, file) {
		u.files = append(u.files, file)
	}
}

func (u *unattributed) err() error {
	if u.blocks == 0 {
		return nil
	}

	slices.Sort(u.files)

	first := u.files
	if len(first) > 3 {
		first = first[:3]
	}

	return fmt.Errorf("%w: %s in %s of the tree's packages map to no declaration of the tree (%s)",
		ErrSource, count(u.blocks, "covered block"), count(len(u.files), "file"), strings.Join(first, ", "))
}

func count(n int, thing string) string {
	if n == 1 {
		return "1 " + thing
	}

	return strconv.Itoa(n) + " " + thing + "s"
}

func (m *mapper) ids(blocks []block) ([]string, error) {
	found := map[string]bool{}

	var lost unattributed

	for _, b := range blocks {
		if !b.ran {
			continue
		}

		dir, ok := m.dirOf(path.Dir(b.file))
		if !ok {
			// another module's or the standard library's code: bound
			// through the packages that import it, not by coverage
			continue
		}

		x, ok := m.index(dir, b.file)
		if !ok {
			lost.add(b.file)

			continue
		}

		id, ok := x.At(token.Position{Filename: filepath.Join(m.root, dir, path.Base(b.file)), Line: b.line, Column: b.col})
		if !ok {
			lost.add(b.file)

			continue
		}

		found[string(id)] = true
	}

	if err := lost.err(); err != nil {
		return nil, err
	}

	out := make([]string, 0, len(found))
	for id := range found {
		out = append(out, id)
	}

	slices.Sort(out)

	return out, nil
}

func (m *mapper) index(dir, file string) (*goid.Index, bool) {
	abs := filepath.Join(m.root, dir, path.Base(file))

	x, ok := m.files[abs]
	if ok {
		return x, x != nil
	}

	f, err := parser.ParseFile(m.fset, abs, nil, parser.SkipObjectResolution)
	if err != nil {
		m.files[abs] = nil

		return nil, false
	}

	x = goid.NewIndex()
	x.Add(m.fset, goid.File(dir, path.Base(file), f))
	m.files[abs] = x

	return x, true
}
