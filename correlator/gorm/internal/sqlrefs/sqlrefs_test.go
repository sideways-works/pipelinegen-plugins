package sqlrefs

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		name  string
		place Place
		sql   string
		want  []Ref
	}{
		{"select with join", Statement, `SELECT n.id FROM notes n JOIN Authors a ON a.id = n.author_id WHERE n.id = ?`,
			[]Ref{{Name: "authors"}, {Name: "notes"}}},
		{"quoted name keeps its case", Statement, `SELECT 1 FROM "Notes"`, []Ref{{Name: "Notes"}}},
		{"schema", Statement, `SELECT 1 FROM archive.notes`, []Ref{{Schema: "archive", Name: "notes"}}},
		{"insert writes", Statement, `INSERT INTO notes (id) SELECT id FROM drafts`,
			[]Ref{{Name: "drafts"}, {Name: "notes", Write: true}}},
		{"update writes", Statement, `UPDATE notes SET body = @body WHERE id IN (SELECT note_id FROM tags)`,
			[]Ref{{Name: "notes", Write: true}, {Name: "tags"}}},
		{"cte is not a relation", Statement, `WITH recent AS (SELECT * FROM notes) SELECT * FROM recent`,
			[]Ref{{Name: "notes"}}},
		{"data-modifying cte", Statement, `WITH gone AS (DELETE FROM notes RETURNING id) SELECT count(*) FROM gone`,
			[]Ref{{Name: "notes", Write: true}}},
		{"several statements", Statement, `DELETE FROM a; TRUNCATE b`,
			[]Ref{{Name: "a", Write: true}, {Name: "b", Write: true}}},
		{"drop", Statement, `DROP TABLE IF EXISTS scratch`, []Ref{{Name: "scratch", Write: true}}},
		{"placeholders in strings stay", Statement, `SELECT '?' FROM notes WHERE body = $tag$ ? $tag$ AND id = ?`,
			[]Ref{{Name: "notes"}}},
		{"jsonb operator without values", Statement, `SELECT 1 FROM notes WHERE meta ? 'pinned'`,
			[]Ref{{Name: "notes"}}},
		{"in with a slice", Statement, `DELETE FROM sessions WHERE subject IN ? AND id <> ?`,
			[]Ref{{Name: "sessions", Write: true}}},
		{"where without subquery", Where, `id = ? AND body LIKE ?`, nil},
		{"where with subquery", Where, `author_id IN (SELECT id FROM authors WHERE active)`, []Ref{{Name: "authors"}}},
		{"select list subquery", Select, `notes.*, (SELECT count(*) FROM tags WHERE tags.note_id = notes.id) AS n`,
			[]Ref{{Name: "tags"}}},
		{"join clause", Join, `LEFT JOIN authors ON authors.id = notes.author_id`, []Ref{{Name: "authors"}}},
		{"from expression", From, `notes AS n`, []Ref{{Name: "notes"}}},
		{"order", Order, `created_at DESC`, nil},
		{"system relations", Statement, `SELECT 1 FROM pg_locks l JOIN pg_catalog.pg_class c ON true, information_schema.tables t, notes`,
			[]Ref{{Name: "notes"}}},
	}

	p := NewParser(nil)

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := p.Parse(c.place, c.sql)
			if got.Err != "" {
				t.Fatalf("parse error: %s", got.Err)
			}

			if !reflect.DeepEqual(got.Refs, c.want) {
				t.Errorf("got %+v, want %+v", got.Refs, c.want)
			}
		})
	}
}

func TestParseError(t *testing.T) {
	if r := NewParser(nil).Parse(Statement, "SELEKT nothing"); r.Err == "" {
		t.Errorf("want a parse error, got %+v", r)
	}
}

func TestParserRemembers(t *testing.T) {
	p := NewParser(nil)
	p.Parse(Statement, "SELECT 1 FROM notes")

	known, dirty := p.Known()
	if !dirty || len(known) != 1 {
		t.Fatalf("known %v dirty %v", known, dirty)
	}

	again := NewParser(known)
	if r := again.Parse(Statement, "SELECT 1 FROM notes"); len(r.Refs) != 1 {
		t.Errorf("cached result lost: %+v", r)
	}

	if _, dirty := again.Known(); dirty {
		t.Error("a cached answer made the cache dirty")
	}
}

func TestPlaceholders(t *testing.T) {
	cases := map[string]string{
		`a = ? AND b = @name`:      `a = $1 AND b = $1`,
		`meta @> ? AND x <@ y`:     `meta @> $1 AND x <@ y`,
		`'it''s ?' -- why?` + "\n": `'it''s ?' -- why?` + "\n",
		`/* ? */ "q?" = $1`:        `/* ? */ "q?" = $1`,
	}

	for in, want := range cases {
		if got := Placeholders(in); got != want {
			t.Errorf("Placeholders(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRoutines(t *testing.T) {
	r := NewParser(nil).Parse(Statement, `SELECT count(*), now(), note_score(n.id), archive.purge(1), pg_catalog.lower('A') FROM notes n; CALL refresh_totals()`)

	want := []Ref{{Name: "note_score"}, {Name: "refresh_totals"}, {Schema: "archive", Name: "purge"}}
	if !reflect.DeepEqual(r.Routines, want) {
		t.Errorf("routines %+v, want %+v", r.Routines, want)
	}
}
