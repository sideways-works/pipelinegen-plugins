package migrations

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func sqlPaths(t *testing.T, root string) []string {
	t.Helper()

	var out []string

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !d.IsDir() && strings.HasSuffix(p, ".sql") {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return fmt.Errorf("relative path: %w", err)
			}

			out = append(out, filepath.ToSlash(rel))
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return out
}

func detect(t *testing.T, root string) ([]Set, []string) {
	t.Helper()

	sets, unknown, err := Detect(root, sqlPaths(t, root))
	if err != nil {
		t.Fatal(err)
	}

	return sets, unknown
}

func files(s Set) []string {
	out := make([]string, 0, len(s.Migrations))
	for _, m := range s.Migrations {
		out = append(out, m.File)
	}

	return out
}

func TestPairs(t *testing.T) {
	sets, unknown := detect(t, "../testdata/notes")

	// queries/notes.sql holds no schema statements, so it is no candidate
	if len(sets) != 1 || len(unknown) != 0 {
		t.Fatalf("sets %+v, unknown %q", sets, unknown)
	}

	s := sets[0]
	if s.Dir != "db/migrations" || s.Layout != Pairs {
		t.Fatalf("got %s %s", s.Dir, s.Layout)
	}

	want := []string{"db/migrations/000001_init.up.sql", "db/migrations/000002_tags.up.sql", "db/migrations/000003_rename.up.sql"}
	if !reflect.DeepEqual(files(s), want) {
		t.Fatalf("got %q", files(s))
	}

	if s.Migrations[0].Down != "db/migrations/000001_init.down.sql" {
		t.Errorf("down %q", s.Migrations[0].Down)
	}
}

func TestLayouts(t *testing.T) {
	cases := []struct {
		root, layout string
		files        []string
		firstSQL     string
	}{
		{"goose", Sections, []string{"migrations/00001_notes.sql", "migrations/00010_tags.sql"}, "CREATE TABLE notes (id int);"},
		{"dbmate", Sections, []string{"db/migrations/20240101120000_notes.sql"}, "CREATE TABLE notes (id int);"},
		{"flyway", Flyway, []string{"sql/V1__notes.sql", "sql/V1.1__title.sql", "sql/V2__tags.sql", "sql/R__views.sql"}, "CREATE TABLE notes (id int);"},
		{"prisma", Dirs, []string{"prisma/migrations/20240101000000_init/migration.sql", "prisma/migrations/20240201000000_tags/migration.sql"}, "CREATE TABLE notes (id int);"},
		{"plain", Numbered, []string{"drizzle/0000_first.sql", "drizzle/0001_second.sql"}, "CREATE TABLE notes (id int);"},
	}

	for _, c := range cases {
		root := filepath.Join("../testdata/layouts", c.root)

		sets, _ := detect(t, root)
		if len(sets) != 1 {
			t.Fatalf("%s: %d sets", c.root, len(sets))
		}

		if sets[0].Layout != c.layout || !reflect.DeepEqual(files(sets[0]), c.files) {
			t.Errorf("%s: got %s %q", c.root, sets[0].Layout, files(sets[0]))
		}

		sql, err := sets[0].Migrations[0].SQL(root)
		if err != nil {
			t.Fatal(err)
		}

		if strings.TrimSpace(sql) != c.firstSQL {
			t.Errorf("%s: up part %q", c.root, sql)
		}
	}
}

func TestUnknownLayout(t *testing.T) {
	sets, unknown := detect(t, "../testdata/layouts/unknown")
	if len(sets) != 0 || !reflect.DeepEqual(unknown, []string{"schema"}) {
		t.Fatalf("sets %+v, unknown %q", sets, unknown)
	}
}

func TestVersionOrder(t *testing.T) {
	for _, c := range [][2]string{{"1", "10"}, {"2", "10"}, {"1.1", "1.2"}, {"1", "1.1"}, {"1.9", "1.10"}, {"20240101", "20240102"}} {
		if !versionLess(c[0], c[1]) || versionLess(c[1], c[0]) {
			t.Errorf("%s before %s", c[0], c[1])
		}
	}
}

func TestTwoMigrationsOneNumber(t *testing.T) {
	_, err := pairs("m", []string{"m/1_a.up.sql", "m/1_b.up.sql"})
	if err == nil {
		t.Fatal("no error")
	}

	if _, err := pairs("m", []string{"m/1_a.down.sql"}); err == nil {
		t.Fatal("no error for a down file alone")
	}
}

func TestClaim(t *testing.T) {
	for _, c := range []struct {
		path, content string
		ok, down      bool
	}{
		{"db/migrations/000001_init.up.sql", "", true, false},
		{"db/migrations/000001_init.down.sql", "", true, true},
		{"sql/V1.1__title.sql", "", true, false},
		{"sql/R__views.sql", "", true, false},
		{"prisma/migrations/20240101000000_init/migration.sql", "", true, false},
		{"migrations/00001_notes.sql", "-- +goose Up\nSELECT 1;", true, false},
		{"drizzle/0000_first.sql", "CREATE TABLE notes (id int);", true, false},
		{"queries/0001_notes.sql", "SELECT * FROM notes;", false, false},
		{"schema/notes.sql", "CREATE TABLE notes (id int);", false, false},
	} {
		ok, down := Claim(c.path, []byte(c.content))
		if ok != c.ok || down != c.down {
			t.Errorf("%s: got %v %v", c.path, ok, down)
		}
	}
}
