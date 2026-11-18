package pglog

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, "2026-01-05T10:00:"+s+"Z")
	if err != nil {
		panic(err)
	}

	return t
}

func TestReadFindsEveryKindOfStatement(t *testing.T) {
	f, err := os.Open("../testdata/server.json")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	})

	sts, err := Read(f, at("01.000"), at("02.000"))
	if err != nil {
		t.Fatal(err)
	}

	got := make([]string, 0, len(sts))
	for _, s := range sts {
		got = append(got, s.Database+" "+s.Text+map[bool]string{true: " (failed)"}[s.Failed])
	}

	want := []string{
		"notes_test_1 SELECT id, title FROM notes WHERE id = $1",
		"notes_test_1 INSERT INTO notes (id, title) VALUES ($1, $2)",
		"notes_test_1 insert into note_events (note_id) values (NEW.id)",
		"notes_test_1 INSERT INTO tags (name) VALUES ($1) (failed)",
		"notes_template ALTER TABLE notes ADD COLUMN body text",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	if !sts[0].Time.Equal(at("01.100")) {
		t.Errorf("time %v", sts[0].Time)
	}
}

func TestReadOpenWindow(t *testing.T) {
	f, err := os.Open("../testdata/server.json")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	})

	sts, err := Read(f, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}

	if len(sts) != 10 {
		t.Fatalf("got %d statements, want 10", len(sts))
	}

	if last := sts[len(sts)-1].Text; last != "SELECT note_score($1) FROM pg_class" {
		t.Errorf("last %q", last)
	}
}

func TestReadRefusesAnotherLogFormat(t *testing.T) {
	_, err := Read(strings.NewReader("2026-01-05 10:00:00.100 UTC [101] LOG:  statement: SELECT 1\n"), time.Time{}, time.Time{})
	if err == nil || !strings.Contains(err.Error(), "jsonlog") {
		t.Fatalf("got %v", err)
	}
}

func TestReadRefusesAnotherTimeZone(t *testing.T) {
	line := `{"timestamp":"2026-01-05 11:00:01.100 CET","dbname":"d","error_severity":"LOG","message":"statement: SELECT 1"}` + "\n"

	_, err := Read(strings.NewReader(line), time.Time{}, time.Time{})
	if !errors.Is(err, ErrNotUTC) {
		t.Fatalf("got %v", err)
	}
}

func TestReadNamesTheLine(t *testing.T) {
	good := `{"timestamp": "2026-01-05 10:00:00.100 UTC", "dbname": "d", "error_severity": "LOG", "message": "statement: SELECT 1"}` + "\n"

	for in, want := range map[string]string{
		good + good[20:]:          "line 2 (byte 119): not a PostgreSQL JSON log line",
		good + "\n" + good[:40]:   "line 3 (byte 120): not a PostgreSQL JSON log line",
		`{"message": "x"}` + "\n": "line 1 (byte 0): not a PostgreSQL JSON log line (log_destination must be jsonlog): no timestamp",
	} {
		if _, err := Read(strings.NewReader(in), time.Time{}, time.Time{}); !errors.Is(err, ErrNotJSONLog) || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", in, err, want)
		}
	}
}

func TestReadKnowsWhenAStatementBegan(t *testing.T) {
	lines := `{"timestamp": "2026-01-05 10:00:01.200 UTC", "dbname": "d", "error_severity": "LOG", "message": "duration: 250.500 ms  plan:\n{\"Query Text\": \"SELECT 1\"}"}
{"timestamp": "2026-01-05 10:00:01.300 UTC", "dbname": "d", "error_severity": "LOG", "message": "duration: 12.000 ms  statement: SELECT 2"}
{"timestamp": "2026-01-05 10:00:01.400 UTC", "dbname": "d", "error_severity": "LOG", "message": "statement: SELECT 3"}
`

	w, err := Scan(strings.NewReader(lines), time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]time.Time{
		"SELECT 1": at("01.200").Add(-250500 * time.Microsecond),
		"SELECT 2": at("01.288"),
		"SELECT 3": at("01.400"),
	}

	for _, s := range w.Statements {
		if !s.Began.Equal(want[s.Text]) {
			t.Errorf("%s began %v, want %v", s.Text, s.Began, want[s.Text])
		}
	}

	if len(w.Statements) != 3 {
		t.Errorf("got %d statements", len(w.Statements))
	}

	if !w.Latest.Equal(at("01.400")) {
		t.Errorf("latest %v", w.Latest)
	}
}

func TestScanSaysTheLatestTimestamp(t *testing.T) {
	f, err := os.Open("../testdata/server.json")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	})

	w, err := Scan(f, at("01.000"), at("02.000"))
	if err != nil {
		t.Fatal(err)
	}

	if len(w.Statements) != 5 || w.Latest.Before(at("02.000")) {
		t.Errorf("%d statements, latest %v", len(w.Statements), w.Latest)
	}
}
