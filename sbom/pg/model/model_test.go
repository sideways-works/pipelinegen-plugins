package model

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

func fixture(t *testing.T, extra ...string) *Model {
	t.Helper()

	files, err := filepath.Glob("../testdata/notes/db/migrations/*.up.sql")
	if err != nil {
		t.Fatal(err)
	}

	sort.Strings(files)

	m := New(nil)

	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}

		m.Apply(filepath.Base(f), string(b))
	}

	for i, sql := range extra {
		m.Apply("extra"+string(rune('a'+i))+".up.sql", sql)
	}

	return m
}

func digests(r Result) map[string]string {
	out := map[string]string{}
	for _, u := range r.Units {
		out[u.ID] = u.Digest + strings.Join(u.DigestFrom, ",")
	}

	return out
}

func ids(r Result) []string {
	out := make([]string, 0, len(r.Units))
	for _, u := range r.Units {
		out = append(out, u.ID)
	}

	return out
}

func TestUnits(t *testing.T) {
	r := fixture(t).Result()

	// the extension's own functions are checked in TestExtensionRoutines
	var own []string

	for _, id := range ids(r) {
		if !strings.HasPrefix(id, "pg:routine:public.citext") {
			own = append(own, id)
		}
	}

	want := []string{
		"pg:data:public.users",
		"pg:extension:citext",
		"pg:function:public.note_written()",
		"pg:function:public.tag_count(bigint)",
		"pg:relation:public.labels",
		"pg:relation:public.note_events",
		"pg:relation:public.note_tags",
		"pg:relation:public.notes",
		"pg:relation:public.published_notes",
		"pg:relation:public.users",
		"pg:routine:public.note_written",
		"pg:routine:public.tag_count",
		"pg:sequence:public.notes_id_seq",
		"pg:sequence:public.tags_id_seq",
		"pg:sequence:public.users_id_seq",
		"pg:table:public.labels",
		"pg:table:public.note_events",
		"pg:table:public.note_tags",
		"pg:table:public.notes",
		"pg:table:public.users",
		"pg:type:public.note_state",
		"pg:view:public.published_notes",
	}
	if !reflect.DeepEqual(own, want) {
		t.Fatalf("units:\n got %q\nwant %q", own, want)
	}

	if len(r.Report.Opaque) != 0 || len(r.Report.Unresolved) != 0 {
		t.Errorf("report: %+v", r.Report)
	}
}

func TestLinks(t *testing.T) {
	r := fixture(t).Result()

	for _, want := range []Link{
		{"pg:table:public.notes", "pg:function:public.note_written()", Uses},
		{"pg:function:public.note_written()", "pg:table:public.note_events", Writes},
		{"pg:view:public.published_notes", "pg:table:public.notes", Reads},
		{"pg:view:public.published_notes", "pg:table:public.users", Reads},
		{"pg:function:public.tag_count(bigint)", "pg:table:public.note_tags", Reads},
		{"pg:table:public.notes", "pg:type:public.note_state", Uses},
		{"pg:table:public.notes", "pg:sequence:public.notes_id_seq", Uses},
		{"pg:table:public.users", "pg:extension:citext", Uses},
		{"pg:relation:public.notes", "pg:table:public.notes", Uses},
		{"pg:routine:public.tag_count", "pg:function:public.tag_count(bigint)", Uses},
		{"file:000003_rename.up.sql", "pg:table:public.labels", Changes},
		{"file:000001_init.up.sql", "pg:data:public.users", Changes},
	} {
		if !slices.Contains(r.Links, want) {
			t.Errorf("missing link %+v", want)
		}
	}

	// a foreign key is no link: following keys would bind every ancestor
	if slices.Contains(r.Links, Link{"pg:table:public.notes", "pg:table:public.users", Uses}) {
		t.Error("a foreign key became a link")
	}
}

func TestChanged(t *testing.T) {
	got := fixture(t).Changed()["000003_rename.up.sql"]

	want := []string{"pg:table:public.labels", "pg:table:public.notes", "pg:table:public.tags"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestANewMigrationChangesExactlyWhatItNames(t *testing.T) {
	before := digests(fixture(t).Result())
	after := digests(fixture(t, "ALTER TABLE note_events ADD COLUMN kind text;").Result())

	var changed []string

	for id, d := range after {
		if before[id] != d {
			changed = append(changed, id)
		}
	}

	sort.Strings(changed)

	if want := []string{"pg:table:public.note_events"}; !reflect.DeepEqual(changed, want) {
		t.Fatalf("changed %q, want %q", changed, want)
	}
}

func TestAForeignKeyChangesItsParent(t *testing.T) {
	before := digests(fixture(t).Result())
	after := digests(fixture(t, "ALTER TABLE note_events ADD FOREIGN KEY (note_id) REFERENCES notes (id) ON DELETE CASCADE;").Result())

	for _, id := range []string{"pg:table:public.note_events", "pg:table:public.notes"} {
		if before[id] == after[id] {
			t.Errorf("%s did not change", id)
		}
	}

	if before["pg:table:public.users"] != after["pg:table:public.users"] {
		t.Error("users changed")
	}
}

func TestSeedRowsChangeOnlyTheData(t *testing.T) {
	before := digests(fixture(t).Result())
	after := digests(fixture(t, "INSERT INTO labels (name) VALUES ('urgent');").Result())

	if before["pg:table:public.labels"] != after["pg:table:public.labels"] {
		t.Error("the table's definition changed")
	}

	if _, ok := after["pg:data:public.labels"]; !ok {
		t.Error("no data unit")
	}
}

func TestFormattingChangesNothing(t *testing.T) {
	a := digests(fixture(t, "ALTER TABLE notes ADD COLUMN body text;").Result())
	b := digests(fixture(t, "-- the body\nALTER TABLE   notes\n  ADD COLUMN body TEXT;").Result())

	if !reflect.DeepEqual(a, b) {
		t.Fatal("whitespace, case of keywords or a comment changed a digest")
	}
}

func TestOpaqueStatementsReachEverything(t *testing.T) {
	m := fixture(t, "DO $$ BEGIN EXECUTE 'ALTER TABLE ' || 'notes ADD COLUMN x int'; END $$;")
	before := digests(fixture(t).Result())
	after := m.Result()

	if len(after.Report.Opaque) != 1 {
		t.Fatalf("opaque: %q", after.Report.Opaque)
	}

	for id, d := range digests(after) {
		if strings.HasPrefix(id, "pg:table:") && before[id] == d {
			t.Errorf("%s did not change", id)
		}
	}
}

func TestADoBlockThatChangesSomething(t *testing.T) {
	before := digests(fixture(t).Result())
	after := digests(fixture(t, `DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM labels WHERE name = 'x') THEN
    ALTER TABLE labels ADD COLUMN color text;
  END IF;
END $$;`).Result())

	if before["pg:table:public.labels"] == after["pg:table:public.labels"] {
		t.Error("labels did not change")
	}

	if before["pg:table:public.notes"] != after["pg:table:public.notes"] {
		t.Error("notes changed")
	}
}

func TestDropsReachDependents(t *testing.T) {
	m := fixture(t, "DROP TABLE users CASCADE;")
	r := m.Result()

	if slices.Contains(ids(r), "pg:table:public.users") || slices.Contains(ids(r), "pg:sequence:public.users_id_seq") {
		t.Error("users or its sequence is still there")
	}

	changed := m.Changed()["extraa.up.sql"]
	for _, id := range []string{"pg:table:public.notes", "pg:view:public.published_notes", "pg:sequence:public.users_id_seq"} {
		if !slices.Contains(changed, id) {
			t.Errorf("%s not changed by the drop: %q", id, changed)
		}
	}
}

func TestDynamicFunction(t *testing.T) {
	r := fixture(t, `CREATE FUNCTION wipe(t text) RETURNS void LANGUAGE plpgsql AS $$
BEGIN EXECUTE format('DELETE FROM %I', t); END $$;`).Result()

	if !reflect.DeepEqual(r.Report.Dynamic, []string{"pg:function:public.wipe(text)"}) {
		t.Fatalf("dynamic: %q", r.Report.Dynamic)
	}
}

func TestSearchPath(t *testing.T) {
	m := New(nil)

	for _, sql := range []string{
		"CREATE SCHEMA app; SET search_path = app, public; CREATE TABLE notes (id int);",
		"CREATE TABLE more (id int);",
		"BEGIN; SET LOCAL search_path = public; CREATE TABLE elsewhere (id int); COMMIT;",
		"CREATE TABLE back_in_app (id int);",
	} {
		m.Apply("f.sql", sql)
	}

	got := ids(m.Result())
	for _, id := range []string{"pg:table:app.notes", "pg:table:app.more", "pg:table:public.elsewhere", "pg:table:app.back_in_app"} {
		if !slices.Contains(got, id) {
			t.Errorf("missing %s in %q", id, got)
		}
	}
}

func TestGeneratedNames(t *testing.T) {
	m := New(nil)

	// the names PostgreSQL 17 gives, shortened to 63 bytes
	sql := `CREATE TABLE a_table_with_a_rather_long_name_that_runs_on_and_on_and_on_xy (
  id int PRIMARY KEY,
  lo int, hi int,
  CHECK (lo < hi),
  CHECK (lo > 0)
);
ALTER TABLE a_table_with_a_rather_long_name_that_runs_on_and_on_and_on_xy DROP CONSTRAINT a_table_with_a_rather_long_name_that_runs_on_and_on_and_o_check;
ALTER TABLE a_table_with_a_rather_long_name_that_runs_on_and_on_and_on_xy DROP CONSTRAINT a_table_with_a_rather_long_name_that_runs_on_and_on_an_lo_check;
CREATE INDEX ON a_table_with_a_rather_long_name_that_runs_on_and_on_and_on_xy (lo, hi);
DROP INDEX a_table_with_a_rather_long_name_that_runs_on_and_on_a_lo_hi_idx;`

	m.Apply("f.sql", sql)

	if r := m.Result().Report; len(r.Unresolved) != 0 {
		t.Fatalf("unresolved: %q", r.Unresolved)
	}
}

func TestTypeStrings(t *testing.T) {
	m := fixture(t, `CREATE FUNCTION f(a int, b varchar(10), c timestamptz, d note_state, e text[], OUT n int) LANGUAGE sql AS 'SELECT 1';`)

	if got := ids(m.Result()); !slices.Contains(got, "pg:function:public.f(integer, character varying, timestamp with time zone, public.note_state, text[])") {
		t.Fatalf("got %q", got)
	}
}

func TestAFileThatDoesNotParseReachesEverything(t *testing.T) {
	before := digests(fixture(t).Result())
	r := fixture(t, "CREATE TABEL x ();").Result()

	if !reflect.DeepEqual(r.Report.Unparsed, []string{"extraa.up.sql"}) {
		t.Fatalf("unparsed: %q", r.Report.Unparsed)
	}

	if before["pg:table:public.notes"] == digests(r)["pg:table:public.notes"] {
		t.Error("notes did not change")
	}
}

func TestExtensionRoutines(t *testing.T) {
	r := fixture(t, "CREATE EXTENSION postgis SCHEMA geo;").Result()

	for _, u := range r.Units {
		switch u.ID {
		case "pg:routine:public.citext_cmp":
			if !reflect.DeepEqual(u.DigestFrom, []string{"pg:extension:citext"}) {
				t.Errorf("citext_cmp: %+v", u)
			}
		case "pg:routine:geo.st_x":
			if !reflect.DeepEqual(u.DigestFrom, []string{"pg:extension:postgis"}) {
				t.Errorf("st_x: %+v", u)
			}
		case "pg:routine:public.lower", "pg:routine:public.st_x":
			t.Errorf("unexpected %s", u.ID)
		}
	}

	if !slices.ContainsFunc(r.Units, func(u Unit) bool { return u.ID == "pg:routine:geo.st_x" }) {
		t.Error("no routine for st_x")
	}
}

func TestReadsThroughAViewReachTheRows(t *testing.T) {
	r := fixture(t).Result()

	if !slices.Contains(r.Links, Link{"pg:view:public.published_notes", "pg:data:public.users", Reads}) {
		t.Error("the view doesn't reach the rows of users")
	}
}

func TestRowsToReaders(t *testing.T) {
	wide := Link{"pg:relation:public.users", "pg:data:public.users", Uses}

	if r := fixture(t).Result(); !slices.Contains(r.Wide, wide) || slices.Contains(r.Links, wide) {
		t.Errorf("the link from the table to its rows is not apart: %+v", r.Wide)
	}
}

func TestNoLinkToRowsNoMigrationWrote(t *testing.T) {
	r := fixture(t, `CREATE TABLE empty_src (n int);
CREATE TABLE copied (n int);
INSERT INTO copied SELECT n FROM empty_src;
CREATE TABLE copied_users (id bigint);
INSERT INTO copied_users SELECT id FROM users;`).Result()

	if !slices.Contains(r.Links, Link{"pg:data:public.copied_users", "pg:data:public.users", Reads}) {
		t.Error("rows copied from users don't reach the rows of users")
	}

	units := map[string]bool{}
	for _, u := range r.Units {
		units[u.ID] = true
	}

	for _, l := range slices.Concat(r.Links, r.Wide) {
		if strings.HasPrefix(l.To, "pg:data:") && !units[l.To] {
			t.Errorf("%s links to %s, which is no unit", l.From, l.To)
		}
	}
}
