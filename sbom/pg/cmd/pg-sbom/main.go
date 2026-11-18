package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/sideways-works/pipelinegen/plugin"

	"github.com/sideways-works/pipelinegen-sbom-pg/migrations"
	"github.com/sideways-works/pipelinegen-sbom-pg/model"
	"github.com/sideways-works/pipelinegen-sbom-pg/pgid"
)

var Version = "dev"

// migrationsByTable is the narrowing under which a slice that depends on
// a migration file depends on the units it touched instead.
const migrationsByTable = "migrations-by-table"

const roleMigration = "migration"

type config struct {
	// Migrations names the detected directories to read, leaving the
	// others out; a layout given is checked against the detected one.
	Migrations []struct {
		Dir    string `json:"dir"`
		Layout string `json:"layout"`
	} `json:"migrations"`
	SearchPath []string `json:"search_path"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return plugin.Serve(args, stdin, stdout, stderr, map[string]plugin.Handler{
		plugin.CallDescribe: func([]byte) (any, error) { return describe(), nil },
		plugin.CallDetect:   detect,
		plugin.CallUnits:    units,
	})
}

func describe() plugin.Description {
	return plugin.Description{
		Protocol:     plugin.Protocol,
		Kind:         plugin.KindSBOM,
		Name:         "pg-sbom",
		Version:      Version,
		Namespaces:   []string{"pg"},
		Capabilities: []string{plugin.CallDetect, plugin.CallUnits},
		Narrowings: []plugin.Narrowing{
			{
				Name:  migrationsByTable,
				OffAt: 5,
				Note:  "a slice that depends on a migration file depends on the tables, views and functions it was seen to touch; it misses what the recording didn't see",
			},
			{
				Name:  model.RowsToReaders,
				OffAt: 2,
				Note:  "a slice that only wrote a table doesn't bind the rows the migrations left in it; an insert that starts conflicting with a new seeded row is missed",
			},
		},
		Candidates: []string{"**/*.sql"},
		Inputs:     map[string]string{plugin.CallDetect: plugin.PerFile, plugin.CallUnits: plugin.PerSet},
	}
}

// detect claims the migration files among the candidates: up files by
// their units, down files whole (a test's setup never runs them).
func detect(in []byte) (any, error) {
	var req plugin.DetectRequest
	if err := json.Unmarshal(in, &req); err != nil {
		return nil, fmt.Errorf("reading the request: %w", err)
	}

	files := []plugin.Claim{}

	for _, p := range req.Paths {
		b, err := os.ReadFile(filepath.Join(req.Tree, filepath.FromSlash(p)))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}

		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", p, err)
		}

		ok, down := migrations.Claim(p, b)
		if !ok {
			continue
		}

		c := plugin.Claim{Path: p, Role: roleMigration}
		if !down {
			c.ByUnits = migrationsByTable
		}

		files = append(files, c)
	}

	return plugin.DetectAnswer{Claims: files}, nil
}

func units(in []byte) (any, error) {
	var req plugin.TreeRequest
	if err := json.Unmarshal(in, &req); err != nil {
		return nil, fmt.Errorf("reading the request: %w", err)
	}

	cfg, err := readConfig(req.Config)
	if err != nil {
		return nil, err
	}

	sets, err := findSets(req, cfg)
	if err != nil {
		return nil, err
	}

	if len(sets) == 0 {
		return nil, fmt.Errorf("%w: no migrations found", plugin.ErrCantTell)
	}

	if len(req.Paths) > 0 && !slices.ContainsFunc(sets, func(s migrations.Set) bool { return holds(s, req.Paths) }) {
		return plugin.UnitsAnswer{Sets: []plugin.Set{}, WithLinks: true}, nil
	}

	// every directory's objects in one answer: two directories (two
	// databases) may name the same table, and an id belongs to one set
	answer, err := buildAll(req.Tree, sets, cfg.SearchPath)
	if err != nil {
		return nil, err
	}

	return plugin.UnitsAnswer{Sets: []plugin.Set{answer}, WithLinks: true}, nil
}

// buildAll answers one set for all directories. A unit two directories
// both make takes its digest from both.
func buildAll(root string, sets []migrations.Set, searchPath []string) (plugin.Set, error) {
	dirs := make([]string, 0, len(sets))
	for _, s := range sets {
		dirs = append(dirs, s.Dir)
	}

	all := plugin.Set{Group: strings.Join(dirs, " + "), Inputs: []string{}, Units: []plugin.Unit{}, Links: []plugin.Link{}}
	index := map[string]int{}
	linked := map[plugin.Link]bool{}

	for _, s := range sets {
		one, err := build(root, s, searchPath)
		if err != nil {
			return all, err
		}

		all.Inputs = append(all.Inputs, one.Inputs...)
		all.CannotTell = append(all.CannotTell, one.CannotTell...)

		for _, u := range one.Units {
			u.Group = all.Group

			i, seen := index[u.ID]
			if !seen {
				index[u.ID] = len(all.Units)
				all.Units = append(all.Units, u)

				continue
			}

			have := &all.Units[i]
			have.Digest = combine(have.Digest, u.Digest)

			for _, from := range u.DigestFrom {
				if !slices.Contains(have.DigestFrom, from) {
					have.DigestFrom = append(have.DigestFrom, from)
				}
			}
		}

		for _, l := range one.Links {
			if !linked[l] {
				linked[l] = true
				all.Links = append(all.Links, l)
			}
		}
	}

	sort.Strings(all.Inputs)
	sort.Slice(all.Units, func(i, j int) bool { return all.Units[i].ID < all.Units[j].ID })

	return all, nil
}

func combine(a, b string) string {
	if a == "" || b == "" {
		return a + b
	}

	sum := sha256.Sum256([]byte(a + "\n" + b))

	return hex.EncodeToString(sum[:])
}

func holds(s migrations.Set, paths []string) bool {
	for _, p := range paths {
		if path.Dir(p) == s.Dir || strings.HasPrefix(p, s.Dir+"/") {
			return true
		}
	}

	return false
}

// labelOf names a unit for people; a variable, so a test can show that
// nothing else in an answer depends on it.
var labelOf = pgid.Label

func build(root string, s migrations.Set, searchPath []string) (plugin.Set, error) {
	m := model.New(searchPath)
	answer := plugin.Set{Group: s.Dir, Inputs: []string{}, Units: []plugin.Unit{}, Links: []plugin.Link{}}

	for _, mig := range s.Migrations {
		sql, err := mig.SQL(root)
		if err != nil {
			return answer, err
		}

		m.Apply(mig.File, sql)

		answer.Inputs = append(answer.Inputs, mig.File)
		if mig.Down != "" {
			answer.Inputs = append(answer.Inputs, mig.Down)
		}
	}

	res := m.Result()

	sort.Strings(answer.Inputs)
	report(s.Dir, res.Report)

	for _, u := range res.Units {
		answer.Units = append(answer.Units, plugin.Unit{
			ID: u.ID, Group: s.Dir, Kind: u.Kind, Digest: u.Digest, DigestFrom: u.DigestFrom, File: u.File,
			Label: labelOf(u.ID),
		})
	}

	for _, l := range res.Links {
		answer.Links = append(answer.Links, plugin.Link{From: l.From, To: l.To, Kind: l.Kind})
	}

	for _, l := range res.Wide {
		answer.Links = append(answer.Links, plugin.Link{From: l.From, To: l.To, Kind: l.Kind, Narrowing: model.RowsToReaders, While: plugin.WhileOff})
	}

	for _, f := range res.Report.Unparsed {
		answer.CannotTell = append(answer.CannotTell, plugin.CannotTell{Unit: "file:" + f, Reason: "PostgreSQL's parser refuses the file"})
	}

	return answer, nil
}

func report(dir string, r model.Report) {
	warnf("%s: %d files, %d statements; %d opaque, %d unresolved, %d dynamic functions, %d files that don't parse",
		dir, r.Files, r.Statements, len(r.Opaque), len(r.Unresolved), len(r.Dynamic), len(r.Unparsed))

	for _, lines := range [][]string{r.Opaque, r.Unresolved, r.Dynamic, r.Unparsed} {
		for _, l := range lines {
			warnf("  %s", l)
		}
	}
}

var errBadLayout = errors.New("unknown layout")

func findSets(req plugin.TreeRequest, cfg config) ([]migrations.Set, error) {
	var paths []string

	if len(req.Claimed) > 0 {
		for _, c := range req.Claimed {
			if c.Role == roleMigration {
				paths = append(paths, c.Path)
			}
		}
	} else {
		var err error

		paths, err = sqlFiles(req.Tree)
		if err != nil {
			return nil, err
		}
	}

	sets, unknown, err := migrations.Detect(req.Tree, paths)
	if err != nil {
		return nil, fmt.Errorf("detecting migrations: %w", err)
	}

	if len(cfg.Migrations) == 0 {
		for _, d := range unknown {
			warnf("%s holds schema statements in no layout pg-sbom knows; it makes no units, so a test that depends on its files depends on each file whole", d)
		}

		return sets, nil
	}

	var out []migrations.Set

	for _, want := range cfg.Migrations {
		dir := filepath.ToSlash(filepath.Clean(want.Dir))

		i := slices.IndexFunc(sets, func(s migrations.Set) bool { return s.Dir == dir })
		if i < 0 {
			return nil, fmt.Errorf("%w: no migrations in a known layout in %s", errBadLayout, want.Dir)
		}

		if want.Layout != "" && want.Layout != sets[i].Layout {
			return nil, fmt.Errorf("%w: %s follows %s, not %s", errBadLayout, dir, sets[i].Layout, want.Layout)
		}

		out = append(out, sets[i])
	}

	return out, nil
}

func sqlFiles(root string) ([]string, error) {
	var out []string

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		name := d.Name()
		if d.IsDir() && p != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || strings.HasPrefix(name, "bazel-")) {
			return filepath.SkipDir
		}

		if !d.IsDir() && strings.HasSuffix(name, ".sql") {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err //nolint:wrapcheck // wrapped below
			}

			out = append(out, filepath.ToSlash(rel))
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing the tree: %w", err)
	}

	return out, nil
}

// readConfig reads the plugin's block strictly: a misspelt key would
// otherwise switch a setting off without a word.
func readConfig(raw map[string]any) (config, error) {
	var cfg config

	b, err := json.Marshal(raw)
	if err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}

	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}

	return cfg, nil
}

// warnf writes a note to stderr; a failed write there leaves nowhere to
// report it.
func warnf(format string, args ...any) {
	if _, err := fmt.Fprintf(os.Stderr, format+"\n", args...); err != nil {
		return
	}
}
