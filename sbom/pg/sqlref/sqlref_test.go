package sqlref

import (
	"reflect"
	"testing"
)

func TestRefs(t *testing.T) {
	cases := []struct {
		sql  string
		want []Ref
	}{
		{
			"SELECT n.title FROM notes n JOIN app.users u ON u.id = n.author WHERE n.id = $1",
			[]Ref{{Schema: "", Name: "notes", Read: true}, {Schema: "app", Name: "users", Read: true}},
		},
		{
			"INSERT INTO notes (title) VALUES ($1)",
			[]Ref{{Name: "notes", Write: true}},
		},
		{
			"INSERT INTO tags (name) VALUES ($1) ON CONFLICT (name) DO NOTHING",
			[]Ref{{Name: "tags", Read: true, Write: true}},
		},
		{
			"UPDATE notes SET title = t.title FROM drafts t WHERE t.id = notes.id",
			[]Ref{{Name: "drafts", Read: true}, {Name: "notes", Read: true, Write: true}},
		},
		{
			"WITH gone AS (DELETE FROM notes WHERE id = $1 RETURNING id) INSERT INTO trash (id) SELECT id FROM gone",
			[]Ref{{Name: "gone", Read: true}, {Name: "notes", Read: true, Write: true}, {Name: "trash", Write: true}},
		},
		{
			"COPY notes FROM STDIN",
			[]Ref{{Name: "notes", Write: true}},
		},
		{
			"COPY notes TO STDOUT",
			[]Ref{{Name: "notes", Read: true}},
		},
		{
			"TRUNCATE notes, tags",
			[]Ref{{Name: "notes", Write: true}, {Name: "tags", Write: true}},
		},
		{
			"SELECT note_count($1), pg_catalog.now(), app.score(1)",
			[]Ref{{Function: true, Name: "note_count", Read: true}, {Function: true, Schema: "app", Name: "score", Read: true}},
		},
		{
			// PostgreSQL's own functions and catalogs are left out
			"SELECT lower(c.relname), count(*) FROM pg_class c, information_schema.tables t, pg_catalog.pg_proc p",
			[]Ref{},
		},
		{
			// a foreign key check as the server runs it
			`SELECT 1 FROM ONLY "public"."notes" x WHERE "id" OPERATOR(pg_catalog.=) $1 FOR KEY SHARE OF x`,
			[]Ref{{Schema: "public", Name: "notes", Read: true}},
		},
		{
			// quoted names keep their case, unquoted ones fold
			`SELECT * FROM "Notes", Tags`,
			[]Ref{{Name: "Notes", Read: true}, {Name: "tags", Read: true}},
		},
		{
			// a PL/pgSQL assignment, as a server logs it
			"NEW.total := (SELECT sum(amount) FROM lines WHERE note_id = NEW.id)",
			[]Ref{{Name: "lines", Read: true}},
		},
	}

	for _, c := range cases {
		got, err := Refs(c.sql)
		if err != nil {
			t.Fatalf("%s: %v", c.sql, err)
		}

		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.sql, got, c.want)
		}
	}
}

func TestRefsRefusesWhatIsNotSQL(t *testing.T) {
	if _, err := Refs("this is not sql at all"); err == nil {
		t.Fatal("no error")
	}
}
