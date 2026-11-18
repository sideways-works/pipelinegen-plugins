// Package migrations finds a repository's migration files by the layout
// their names follow (golang-migrate, goose, dbmate, Flyway, Prisma, Drizzle,
// Atlas) and puts them in the order a migration tool runs them.
package migrations

import (
	"errors"
	"fmt"
	"math/big"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const prismaFile = "migration.sql"

const (
	Pairs     = "pairs"
	Sections  = "sections"
	Flyway    = "flyway"
	Dirs      = "dirs"
	Numbered  = "numbered"
	Repeating = "repeatable"
)

type Migration struct {
	Version string
	Name    string
	// File holds the change, relative to the repository's root.
	File string
	// Down is the file that undoes it, for pairs.
	Down string
	// Layout says how to read File: for sections, only the up part runs.
	Layout string
}

type Set struct {
	Dir        string
	Layout     string
	Migrations []Migration
}

var (
	pairName    = regexp.MustCompile(`^([0-9]+)_(.+)\.(up|down)\.sql$`)
	numberedSQL = regexp.MustCompile(`^([0-9]+)_(.+)\.sql$`)
	flywayV     = regexp.MustCompile(`^V([0-9][0-9._]*)__(.+)\.sql$`)
	flywayR     = regexp.MustCompile(`^R__(.+)\.sql$`)
	dirName     = regexp.MustCompile(`^([0-9]+)_(.+)$`)
	upMarker    = regexp.MustCompile(`(?m)^\s*--\s*(\+goose\s+Up|migrate:up)\b.*$`)
	downMarker  = regexp.MustCompile(`(?m)^\s*--\s*(\+goose\s+Down|migrate:down)\b.*$`)
)

var (
	errDuplicate = errors.New("two migrations share a number")
	errNoUp      = errors.New("a migration has no up file")
)

var ErrNoLayout = errors.New("no known migration layout")

// Detect groups the given .sql paths (relative to root) by directory and
// returns each directory whose files follow a known layout. Directories
// with schema statements but no known layout are returned in unknown.
func Detect(root string, paths []string) (sets []Set, unknown []string, err error) {
	byDir := map[string][]string{}

	for _, p := range paths {
		p = filepath.ToSlash(p)
		if !strings.HasSuffix(p, ".sql") {
			continue
		}

		dir := path.Dir(p)
		// a Prisma migration's directory is the version: group by its parent
		if path.Base(p) == prismaFile && dirName.MatchString(path.Base(dir)) {
			dir = path.Dir(dir)
		}

		byDir[dir] = append(byDir[dir], p)
	}

	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}

	sort.Strings(dirs)

	for _, d := range dirs {
		set, err := classify(root, d, byDir[d])

		switch {
		case errors.Is(err, ErrNoLayout):
			if holdsSchema(root, byDir[d]) {
				unknown = append(unknown, d)
			}
		case err != nil:
			return nil, nil, err
		default:
			if holdsSchema(root, byDir[d]) {
				sets = append(sets, set)
			}
		}
	}

	return sets, unknown, nil
}

func classify(root, dir string, files []string) (Set, error) {
	set := Set{Dir: dir}

	switch {
	case all(files, func(n string) bool { return pairName.MatchString(n) }):
		return pairs(dir, files)
	case all(files, func(n string) bool { return n == prismaFile }):
		set.Layout = Dirs

		for _, f := range files {
			m := dirName.FindStringSubmatch(path.Base(path.Dir(f)))
			set.Migrations = append(set.Migrations, Migration{Version: m[1], Name: m[2], File: f, Layout: Dirs})
		}
	case all(files, func(n string) bool { return flywayV.MatchString(n) || flywayR.MatchString(n) }):
		set.Layout = Flyway

		var repeatable []Migration

		for _, f := range files {
			if m := flywayV.FindStringSubmatch(path.Base(f)); m != nil {
				set.Migrations = append(set.Migrations, Migration{Version: m[1], Name: m[2], File: f, Layout: Flyway})
			} else if m := flywayR.FindStringSubmatch(path.Base(f)); m != nil {
				repeatable = append(repeatable, Migration{Name: m[1], File: f, Layout: Repeating})
			}
		}

		sortMigrations(set.Migrations)
		sort.Slice(repeatable, func(i, j int) bool { return repeatable[i].Name < repeatable[j].Name })
		set.Migrations = append(set.Migrations, repeatable...)

		return set, nil
	case all(files, func(n string) bool { return numberedSQL.MatchString(n) }):
		sections, err := anySections(root, files)
		if err != nil {
			return set, err
		}

		set.Layout = Numbered
		if sections {
			set.Layout = Sections
		}

		for _, f := range files {
			m := numberedSQL.FindStringSubmatch(path.Base(f))
			set.Migrations = append(set.Migrations, Migration{Version: m[1], Name: m[2], File: f, Layout: set.Layout})
		}
	default:
		return set, fmt.Errorf("%s: %w", dir, ErrNoLayout)
	}

	sortMigrations(set.Migrations)

	return set, nil
}

func all(files []string, ok func(string) bool) bool {
	for _, f := range files {
		if !ok(path.Base(f)) {
			return false
		}
	}

	return len(files) > 0
}

func pairs(dir string, files []string) (Set, error) {
	set := Set{Dir: dir, Layout: Pairs}
	byVersion := map[string]*Migration{}

	for _, f := range files {
		m := pairName.FindStringSubmatch(path.Base(f))

		mig := byVersion[m[1]]
		if mig == nil {
			mig = &Migration{Version: m[1], Name: m[2], Layout: Pairs}
			byVersion[m[1]] = mig
		} else if mig.Name != m[2] {
			return set, fmt.Errorf("%w: %s: %s (%s and %s)", errDuplicate, dir, m[1], mig.Name, m[2])
		}

		if m[3] == "up" {
			mig.File = f
		} else {
			mig.Down = f
		}
	}

	for _, m := range byVersion {
		if m.File == "" {
			return set, fmt.Errorf("%w: %s: %s (%s)", errNoUp, dir, m.Version, m.Name)
		}

		set.Migrations = append(set.Migrations, *m)
	}

	sortMigrations(set.Migrations)

	return set, nil
}

// sortMigrations orders by version: as numbers when both are numbers
// (dotted parts compared one by one, as Flyway does), else as text.
func sortMigrations(ms []Migration) {
	sort.SliceStable(ms, func(i, j int) bool { return versionLess(ms[i].Version, ms[j].Version) })
}

func versionLess(a, b string) bool {
	pa := strings.FieldsFunc(a, func(r rune) bool { return r == '.' || r == '_' })
	pb := strings.FieldsFunc(b, func(r rune) bool { return r == '.' || r == '_' })

	for k := 0; k < len(pa) && k < len(pb); k++ {
		x, okx := new(big.Int).SetString(pa[k], 10)
		y, oky := new(big.Int).SetString(pb[k], 10)

		if !okx || !oky {
			if pa[k] != pb[k] {
				return pa[k] < pb[k]
			}

			continue
		}

		if c := x.Cmp(y); c != 0 {
			return c < 0
		}
	}

	return len(pa) < len(pb)
}

func anySections(root string, files []string) (bool, error) {
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(root, f)) //nolint:gosec // a file of the repository being read
		if err != nil {
			return false, fmt.Errorf("reading %s: %w", f, err)
		}

		if upMarker.Match(b) {
			return true, nil
		}
	}

	return false, nil
}

// schemaStatement finds DDL, so that a directory of queries or fixtures
// is not taken for migrations.
var schemaStatement = regexp.MustCompile(`(?i)\b(CREATE|ALTER|DROP)\s+(TABLE|INDEX|UNIQUE\s+INDEX|VIEW|MATERIALIZED\s+VIEW|TYPE|SEQUENCE|EXTENSION|SCHEMA|DOMAIN|(OR\s+REPLACE\s+)?FUNCTION|(OR\s+REPLACE\s+)?TRIGGER)\b`)

func holdsSchema(root string, files []string) bool {
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(root, f)) //nolint:gosec // a file of the repository being read
		if err == nil && schemaStatement.Match(b) {
			return true
		}
	}

	return false
}

// SQL reads the part of a migration that changes the database: the whole
// file, or the up section.
func (m Migration) SQL(root string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, m.File)) //nolint:gosec // a file of the repository being read
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", m.File, err)
	}

	s := string(b)
	if m.Layout != Sections {
		return s, nil
	}

	up := upMarker.FindStringIndex(s)
	if up == nil {
		return "", nil
	}

	s = s[up[1]:]
	if down := downMarker.FindStringIndex(s); down != nil {
		s = s[:down[0]]
	}

	return s, nil
}

// Claim tells whether a file is a migration from its path and content alone.
// A plain numbered file needs a section marker or a schema statement too;
// down is true for a file that only undoes a change.
func Claim(p string, content []byte) (ok, down bool) {
	base := path.Base(p)

	switch {
	case pairName.MatchString(base):
		return true, pairName.FindStringSubmatch(base)[3] == "down"
	case flywayV.MatchString(base) || flywayR.MatchString(base):
		return true, false
	case base == prismaFile && dirName.MatchString(path.Base(path.Dir(p))):
		return true, false
	case numberedSQL.MatchString(base):
		return upMarker.Match(content) || schemaStatement.Match(content), false
	default:
		return false, false
	}
}
