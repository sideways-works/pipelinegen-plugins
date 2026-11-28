package collect

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func at(s string) func() time.Time {
	return func() time.Time {
		t, err := time.Parse(time.RFC3339Nano, "2026-01-05T10:00:"+s+"Z")
		if err != nil {
			panic(err)
		}

		return t
	}
}

func logLines(t *testing.T) []string {
	t.Helper()

	b, err := os.ReadFile("../testdata/server.json")
	if err != nil {
		t.Fatal(err)
	}

	return strings.SplitAfter(strings.TrimSuffix(string(b), "\n"), "\n")
}

func appendLog(t *testing.T, name string, lines []string) {
	t.Helper()

	f, err := os.OpenFile(name, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.WriteString(strings.Join(lines, "")); err != nil {
		t.Fatal(err)
	}

	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func part(t *testing.T, cfg Config, dir string) Part {
	t.Helper()

	p, err := Read(cfg, dir)
	if err != nil {
		t.Fatal(err)
	}

	return p
}

func textsOf(sts []Statement) []string {
	out := make([]string, 0, len(sts))
	for _, s := range sts {
		out = append(out, s.Text)
	}

	return out
}

func own(t *testing.T, cfg Config, dir string) []string {
	t.Helper()

	return textsOf(part(t, cfg, dir).Own)
}

func TestSetupThenSlices(t *testing.T) {
	logDir := t.TempDir()
	log := filepath.Join(logDir, "postgresql-2026-01-05_100000.json")
	lines := logLines(t)
	cfg := Config{Log: logDir, SetupDatabases: []string{"*_template"}, Databases: []string{"notes_test_*"}, Quiet: Duration(10 * time.Millisecond)}
	cov := t.TempDir()

	appendLog(t, log, lines[:3])

	if err := Setup(cfg, filepath.Join(cov, "_setup"), at("00.900")); err != nil {
		t.Fatal(err)
	}

	if got := own(t, cfg, filepath.Join(cov, "_setup")); !reflect.DeepEqual(got, []string{"INSERT INTO users (name) VALUES ('seed')"}) {
		t.Errorf("setup: %q", got)
	}

	if got := textsOf(part(t, cfg, filepath.Join(cov, "_setup")).Setup); len(got) != 1 {
		t.Errorf("setup databases: %q", got)
	}

	one := filepath.Join(cov, "one")
	if err := Start(cfg, one, at("01.000")); err != nil {
		t.Fatal(err)
	}

	appendLog(t, log, lines[3:9])

	if err := End(cfg, one, "one", at("02.000")); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"INSERT INTO notes (id, title) VALUES ($1, $2)",
		"INSERT INTO tags (name) VALUES ($1)",
		"SELECT id, title FROM notes WHERE id = $1",
		"insert into note_events (note_id) values (NEW.id)",
	}
	if got := own(t, cfg, one); !reflect.DeepEqual(got, want) {
		t.Errorf("first slice:\n got %q\nwant %q", got, want)
	}

	// the template migrated again during the slice counts as setup
	if got := textsOf(part(t, cfg, one).Setup); !reflect.DeepEqual(got, []string{"ALTER TABLE notes ADD COLUMN body text"}) {
		t.Errorf("first slice, setup databases: %q", got)
	}

	sts := part(t, cfg, one).Own

	if !sts[1].Failed {
		t.Errorf("the refused insert is not marked failed: %+v", sts[1])
	}

	two := filepath.Join(cov, "two")
	if err := Start(cfg, two, at("02.000")); err != nil {
		t.Fatal(err)
	}

	appendLog(t, log, lines[9:])

	if err := End(cfg, two, "two", at("03.000")); err != nil {
		t.Fatal(err)
	}

	want = []string{"SELECT count(*) FROM tags", "SELECT note_score($1) FROM pg_class"}
	if got := own(t, cfg, two); !reflect.DeepEqual(got, want) {
		t.Errorf("second slice:\n got %q\nwant %q", got, want)
	}
}

func TestStatementsOutsideTheWindowStayOut(t *testing.T) {
	log := filepath.Join(t.TempDir(), "server.json")
	appendLog(t, log, logLines(t))

	dir := t.TempDir()
	cfg := Config{Log: log, Quiet: Duration(10 * time.Millisecond)}

	// a mark at the very start of the file, but a window that holds only
	// the second slice's time
	if err := writeJSON(filepath.Join(dir, StartFile), mark{Time: at("02.000")(), File: log}); err != nil {
		t.Fatal(err)
	}

	if err := End(cfg, dir, "s", at("02.150")); err != nil {
		t.Fatal(err)
	}

	if got := own(t, cfg, dir); !reflect.DeepEqual(got, []string{"SELECT count(*) FROM tags"}) {
		t.Errorf("got %q", got)
	}
}

func TestEndWithoutStart(t *testing.T) {
	log := filepath.Join(t.TempDir(), "server.json")
	appendLog(t, log, logLines(t))

	err := End(Config{Log: log}, t.TempDir(), "s", at("01.000"))
	if err == nil || !strings.Contains(err.Error(), "slice-start") {
		t.Fatalf("got %v", err)
	}
}

func TestMissingLog(t *testing.T) {
	if err := Start(Config{Log: filepath.Join(t.TempDir(), "none")}, t.TempDir(), at("01.000")); err == nil {
		t.Fatal("no error for a log that doesn't exist")
	}
}

func TestWithoutALog(t *testing.T) {
	dir := t.TempDir()

	end := func(cfg Config, dir string, now Clock) error { return End(cfg, dir, "s", now) }

	for _, run := range []func(Config, string, Clock) error{Setup, Start, end} {
		if err := run(Config{}, dir, at("01.000")); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := os.Stat(filepath.Join(dir, UnconfiguredFile)); err != nil {
		t.Fatalf("no marker: %v", err)
	}
}

func TestDuration(t *testing.T) {
	var d Duration
	if err := d.UnmarshalJSON([]byte(`"250ms"`)); err != nil || time.Duration(d) != 250*time.Millisecond {
		t.Fatalf("got %v %v", d, err)
	}

	if err := d.UnmarshalJSON([]byte(`250`)); err == nil {
		t.Fatal("a bare number is accepted")
	}
}

func TestEndWaitsForTheLogToGoQuiet(t *testing.T) {
	log := filepath.Join(t.TempDir(), "server.json")
	lines := logLines(t)
	appendLog(t, log, lines[:9])

	dir := t.TempDir()
	cfg := Config{Log: log, Quiet: Duration(400 * time.Millisecond)}

	if err := Start(cfg, dir, at("02.000")); err != nil {
		t.Fatal(err)
	}

	late := make(chan error, 1)

	go func() {
		time.Sleep(20 * time.Millisecond)

		f, err := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			late <- err

			return
		}

		_, err = f.WriteString(strings.Join(lines[9:], ""))
		if cerr := f.Close(); err == nil {
			err = cerr
		}

		late <- err
	}()

	if err := End(cfg, dir, "s", at("03.000")); err != nil {
		t.Fatal(err)
	}

	if err := <-late; err != nil {
		t.Fatal(err)
	}

	want := []string{"SELECT count(*) FROM tags", "SELECT note_score($1) FROM pg_class"}
	if got := own(t, cfg, dir); !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLogDirectoryWithoutJSON(t *testing.T) {
	logDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(logDir, "postgresql.log"), []byte("LOG: statement: SELECT 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := Config{Log: logDir, Quiet: Duration(time.Millisecond)}

	for name, run := range map[string]func(Config, string, Clock) error{"setup-end": Setup, "slice-start": Start} {
		if err := run(cfg, t.TempDir(), at("01.000")); !errors.Is(err, ErrNoLog) || !strings.Contains(err.Error(), "log_destination=jsonlog") {
			t.Errorf("%s: %v, want a failure naming log_destination=jsonlog", name, err)
		}
	}

	appendLog(t, filepath.Join(logDir, "postgresql.json"), logLines(t))

	if err := Start(cfg, t.TempDir(), at("01.000")); err != nil {
		t.Errorf("with a JSON file: %v", err)
	}
}

func TestAnUnreadableLogCannotTell(t *testing.T) {
	lines := logLines(t)
	tail := lines[0][len(lines[0])/2:]

	for name, c := range map[string]struct {
		// before is the log at slice-start, during what happens to it
		// until slice-end
		before, during func(log string)
		want           []string
	}{
		"a part starting with a line's tail": {
			before: func(log string) { appendLog(t, log, lines) },
			during: func(log string) {
				if err := os.Truncate(log, 0); err != nil {
					t.Fatal(err)
				}

				appendLog(t, log, []string{tail, lines[1]})
			},
			want: []string{"postgresql.json: line 1 (byte 0):", "looks cut", "log_destination must be jsonlog"},
		},
		"a part ending in a cut line": {
			before: func(log string) { appendLog(t, log, lines[:2]) },
			during: func(log string) { appendLog(t, log, []string{lines[2], lines[3][:len(lines[3])/2]}) },
			want:   []string{"postgresql.json, read from byte ", ": line 2 (byte ", "looks cut", "log_destination must be jsonlog"},
		},
		"a log that isn't jsonlog": {
			before: func(log string) { appendLog(t, log, nil) },
			during: func(log string) {
				appendLog(t, log, []string{"2026-01-05 10:00:00.100 UTC [101] LOG:  statement: SELECT 1\n"})
			},
			want: []string{"postgresql.json: line 1 (byte 0):", "isn't jsonlog", "log_destination must be jsonlog"},
		},
		"a clean part": {
			before: func(log string) { appendLog(t, log, lines[:2]) },
			during: func(log string) { appendLog(t, log, lines[2:4]) },
		},
	} {
		logDir, dir := t.TempDir(), t.TempDir()
		log := filepath.Join(logDir, "postgresql.json")
		cfg := Config{Log: logDir, Quiet: Duration(time.Millisecond)}

		c.before(log)

		if err := Start(cfg, dir, at("00.000")); err != nil {
			t.Fatalf("%s: start: %v", name, err)
		}

		c.during(log)

		if err := End(cfg, dir, "s", at("01.000")); err != nil {
			t.Errorf("%s: end failed: %v", name, err)

			continue
		}

		_, err := Read(cfg, dir)

		if c.want == nil {
			if err != nil {
				t.Errorf("%s: %v", name, err)
			} else if got := own(t, cfg, dir); len(got) == 0 {
				t.Errorf("%s: no statements", name)
			}

			continue
		}

		if !errors.Is(err, ErrCannotTell) {
			t.Errorf("%s: %v, want can't tell", name, err)

			continue
		}

		for _, w := range c.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: reason %q lacks %q", name, err, w)
			}
		}
	}
}

func logLine(ts, msg string) string {
	b, err := json.Marshal(map[string]string{
		"timestamp": "2026-01-05 10:00:" + ts + " UTC", "dbname": "notes_test_1",
		"error_severity": "LOG", "message": msg,
	})
	if err != nil {
		panic(err)
	}

	return string(b) + "\n"
}

func TestAStatementThatBeganBeforeTheStartIsNotTheSlices(t *testing.T) {
	log := filepath.Join(t.TempDir(), "server.json")
	appendLog(t, log, nil)

	dir := t.TempDir()
	cfg := Config{Log: log, Quiet: Duration(time.Millisecond)}

	if err := Start(cfg, filepath.Join(dir, "sql"), at("01.000")); err != nil {
		t.Fatal(err)
	}

	appendLog(t, log, []string{
		// began at 00.600, before the start
		logLine("01.100", `duration: 500.000 ms  plan:
{"Query Text": "SELECT 1 FROM queue"}`),
		// began within the millisecond before its line, after the start
		logLine("01.200", `duration: 0.050 ms  plan:
{"Query Text": "SELECT 1 FROM notes"}`),
		// began less than a millisecond before the start, as far as the
		// log's milliseconds can tell: it may be the slice's
		logLine("01.100", `duration: 100.500 ms  plan:
{"Query Text": "SELECT 1 FROM tags"}`),
		logLine("01.300", "statement: DELETE FROM notes"),
	})

	if err := End(cfg, filepath.Join(dir, "sql"), "s", at("02.000")); err != nil {
		t.Fatal(err)
	}

	want := []string{"DELETE FROM notes", "SELECT 1 FROM notes", "SELECT 1 FROM tags"}
	if got := own(t, cfg, filepath.Join(dir, "sql")); !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWindowsDontShareAMillisecond(t *testing.T) {
	log := filepath.Join(t.TempDir(), "server.json")
	appendLog(t, log, nil)

	cfg := Config{Log: log, Quiet: Duration(time.Microsecond)}

	for range 20 {
		dir := t.TempDir()
		if err := Start(cfg, dir, time.Now); err != nil {
			t.Fatal(err)
		}

		before := time.Now()

		if err := End(cfg, dir, "s", time.Now); err != nil {
			t.Fatal(err)
		}

		if end := before.Truncate(time.Millisecond).Add(time.Millisecond); time.Now().Before(end) {
			t.Fatalf("slice-end returned before %v, the end of its window", end)
		}
	}
}

func TestARotatedLogIsReadWhole(t *testing.T) {
	logDir := t.TempDir()
	first := filepath.Join(logDir, "postgresql-b.json")
	appendLog(t, first, []string{logLine("00.500", "statement: SELECT 'before'")})

	cfg := Config{Log: logDir, Quiet: Duration(time.Millisecond)}

	// a name that sorts before the start's file
	dir := t.TempDir()
	if err := Start(cfg, dir, at("01.000")); err != nil {
		t.Fatal(err)
	}

	appendLog(t, filepath.Join(logDir, "postgresql-a.json"), []string{logLine("01.100", "statement: SELECT 'rotated'")})

	if err := End(cfg, dir, "s", at("02.000")); err != nil {
		t.Fatal(err)
	}

	if got := own(t, cfg, dir); !reflect.DeepEqual(got, []string{"SELECT 'rotated'"}) {
		t.Errorf("rotated: got %q", got)
	}

	// the start's file emptied and written again, longer than before
	dir = t.TempDir()
	if err := Start(cfg, dir, at("03.000")); err != nil {
		t.Fatal(err)
	}

	if err := os.Truncate(first, 0); err != nil {
		t.Fatal(err)
	}

	appendLog(t, first, []string{
		logLine("03.100", "statement: SELECT 'again, at the beginning of the file'"),
		logLine("03.200", "statement: SELECT 'again'"),
	})

	if err := End(cfg, dir, "s", at("04.000")); err != nil {
		t.Fatal(err)
	}

	want := []string{"SELECT 'again'", "SELECT 'again, at the beginning of the file'"}
	if got := own(t, cfg, dir); !reflect.DeepEqual(got, want) {
		t.Errorf("emptied: got %q, want %q", got, want)
	}
}

func TestASliceThatRanBeforeItsStartCannotTell(t *testing.T) {
	log := filepath.Join(t.TempDir(), "server.json")
	appendLog(t, log, logLines(t))

	cfg := Config{Log: log, Quiet: Duration(time.Millisecond)}

	ran := t.TempDir()
	if err := os.WriteFile(filepath.Join(ran, "tests.coverprofile"), []byte("mode: atomic\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	clean := t.TempDir()
	if err := os.MkdirAll(filepath.Join(clean, "go", "gocoverdir"), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(clean, "go", "start"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	for part, wantMarker := range map[string]bool{ran: true, clean: false} {
		dir := filepath.Join(part, "sql")

		if err := Start(cfg, dir, at("02.000")); err != nil {
			t.Fatal(err)
		}

		if err := End(cfg, dir, "s", at("03.000")); err != nil {
			t.Fatal(err)
		}

		why, err := os.ReadFile(filepath.Join(dir, UnboundedFile))
		if !wantMarker {
			if err == nil {
				t.Errorf("a slice that hadn't run is marked: %s", why)
			}

			continue
		}

		if err != nil {
			t.Fatalf("no marker: %v", err)
		}

		for _, w := range []string{"tests.coverprofile", "slice-start came after", "batch: false"} {
			if !strings.Contains(string(why), w) {
				t.Errorf("reason %q lacks %q", why, w)
			}
		}

		if HasWindow(dir) {
			t.Error("a window noted next to the marker")
		}
	}
}

func TestAServerClockAheadCannotTell(t *testing.T) {
	log := filepath.Join(t.TempDir(), "server.json")
	appendLog(t, log, logLines(t))

	dir := t.TempDir()
	cfg := Config{Log: log, Quiet: Duration(time.Millisecond)}

	if err := Start(cfg, dir, at("01.000")); err != nil {
		t.Fatal(err)
	}

	future := strings.Replace(logLine("01.500", "statement: SELECT 1"), "2026-01-05", "2099-01-05", 1)
	appendLog(t, log, []string{future})

	if err := End(cfg, dir, "s", at("02.000")); err != nil {
		t.Fatal(err)
	}

	_, err := Read(cfg, dir)
	if !errors.Is(err, ErrCannotTell) {
		t.Fatalf("%v, want can't tell", err)
	}

	for _, w := range []string{"server.json", "2099-01-05 10:00:01.500", "this machine's clock", "same clock"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("reason %q lacks %q", err, w)
		}
	}
}

func slicePart(cov, slice string, k int) string {
	if k == 1 {
		return filepath.Join(cov, slice, "sql")
	}

	return filepath.Join(cov, ".pipelinegen", "part-"+strconv.Itoa(k), slice, "sql")
}

func TestTheLogIsLinkedOncePerBatch(t *testing.T) {
	logDir, cov := t.TempDir(), t.TempDir()
	log := filepath.Join(logDir, "postgresql.json")
	appendLog(t, log, []string{logLine("00.500", "statement: SELECT 'setup'")})

	cfg := Config{Log: logDir, Quiet: Duration(time.Millisecond)}

	if err := Setup(cfg, filepath.Join(cov, "_setup", "sql"), at("00.900")); err != nil {
		t.Fatal(err)
	}

	for i, dir := range []string{slicePart(cov, "api/a_test.go", 1), slicePart(cov, "api/a_test.go", 2)} {
		ts := fmt.Sprintf("0%d.", i+1)

		if err := Start(cfg, dir, at(ts+"000")); err != nil {
			t.Fatal(err)
		}

		appendLog(t, log, []string{logLine(ts+"500", fmt.Sprintf("statement: SELECT %d", i+1))})

		if err := End(cfg, dir, "api/a_test.go", at(ts+"900")); err != nil {
			t.Fatal(err)
		}

		if got := own(t, cfg, dir); !reflect.DeepEqual(got, []string{fmt.Sprintf("SELECT %d", i+1)}) {
			t.Errorf("part %d: %q", i+1, got)
		}

		if _, err := os.Stat(filepath.Join(dir, LogDir)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("part %d keeps a log of its own: %v", i+1, err)
		}
	}

	links, err := os.ReadDir(filepath.Join(cov, "_setup", "sql", LogDir))
	if err != nil || len(links) != 1 {
		t.Fatalf("links %v %v, want one", links, err)
	}

	if got := own(t, cfg, filepath.Join(cov, "_setup", "sql")); !reflect.DeepEqual(got, []string{"SELECT 'setup'"}) {
		t.Errorf("setup: %q", got)
	}
}

func TestTheRecordingOutlivesTheServersFiles(t *testing.T) {
	logDir := t.TempDir()
	log := filepath.Join(logDir, "postgresql.json")
	appendLog(t, log, []string{logLine("00.500", "statement: SELECT 'before'")})

	cfg := Config{Log: logDir, Quiet: Duration(time.Millisecond)}

	emptied := t.TempDir()
	if err := Start(cfg, emptied, at("02.000")); err != nil {
		t.Fatal(err)
	}

	appendLog(t, log, []string{logLine("02.500", "statement: SELECT 'written over'")})

	if err := End(cfg, emptied, "s", at("02.900")); err != nil {
		t.Fatal(err)
	}

	// the server empties its file and writes it again, then the set-up
	// removes it
	if err := os.Truncate(log, 0); err != nil {
		t.Fatal(err)
	}

	appendLog(t, log, []string{logLine("03.500", "statement: SELECT 'another, longer line in its place'")})

	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}

	_, err := Read(cfg, emptied)
	if !errors.Is(err, ErrCannotTell) || !strings.Contains(err.Error(), "postgresql.json") || !strings.Contains(err.Error(), "emptied or written over") {
		t.Errorf("emptied: %v", err)
	}

	// a file the set-up removed without writing over it reads as noted
	log2 := filepath.Join(logDir, "postgresql-2.json")
	appendLog(t, log2, nil)

	gone := t.TempDir()
	if err := Start(cfg, gone, at("04.000")); err != nil {
		t.Fatal(err)
	}

	appendLog(t, log2, []string{logLine("04.500", "statement: SELECT 'kept'")})

	if err := End(cfg, gone, "s", at("04.900")); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(log2); err != nil {
		t.Fatal(err)
	}

	if got := own(t, cfg, gone); !reflect.DeepEqual(got, []string{"SELECT 'kept'"}) {
		t.Errorf("removed: %q", got)
	}
}

func TestACopyWhereNoLinkCanBeMade(t *testing.T) {
	linkFile = func(string, string) error { return os.ErrPermission }

	var said strings.Builder

	Log = &said

	t.Cleanup(func() { linkFile, Log = os.Link, os.Stderr })

	log := filepath.Join(t.TempDir(), "server.json")
	before := logLine("00.500", "statement: SELECT 'before'")
	appendLog(t, log, []string{before})

	cfg := Config{Log: log, Quiet: Duration(time.Millisecond)}
	dir := t.TempDir()

	if err := Start(cfg, dir, at("01.000")); err != nil {
		t.Fatal(err)
	}

	during := logLine("01.500", "statement: SELECT 'during'")
	appendLog(t, log, []string{during})

	if err := End(cfg, dir, "s", at("01.900")); err != nil {
		t.Fatal(err)
	}

	copies, err := os.ReadDir(filepath.Join(dir, LogDir))
	if err != nil || len(copies) != 1 {
		t.Fatalf("copies %v %v", copies, err)
	}

	if info, err := copies[0].Info(); err != nil || info.Size() != int64(len(during)) {
		t.Errorf("the copy holds %v bytes, want the window's %d", info, len(during))
	}

	if got := own(t, cfg, dir); !reflect.DeepEqual(got, []string{"SELECT 'during'"}) {
		t.Errorf("got %q", got)
	}

	if !strings.Contains(said.String(), "server.json can't be linked into the coverage output") {
		t.Errorf("the copy isn't said: %q", said.String())
	}
}

func TestEndStopsWaitingOnceTheLogHoldsItsEnd(t *testing.T) {
	log := filepath.Join(t.TempDir(), "server.json")
	appendLog(t, log, []string{logLine("00.500", "statement: SELECT 'before'")})

	// a log that never goes quiet in time: only the line after the end
	// lets the end go at once
	cfg := Config{Log: log, Quiet: Duration(time.Minute)}
	dir := t.TempDir()

	if err := Start(cfg, dir, at("01.000")); err != nil {
		t.Fatal(err)
	}

	appendLog(t, log, []string{
		logLine("01.500", "statement: SELECT 'during'"),
		logLine("02.500", "statement: SELECT 'another slice'"),
	})

	began := time.Now()

	if err := End(cfg, dir, "s", at("02.000")); err != nil {
		t.Fatal(err)
	}

	if took := time.Since(began); took > 30*time.Second {
		t.Errorf("End took %s with the log past its end", took)
	}

	if got := own(t, cfg, dir); !reflect.DeepEqual(got, []string{"SELECT 'during'"}) {
		t.Errorf("got %q", got)
	}
}

func TestACutLastLineLeavesItToTheQuietTime(t *testing.T) {
	log := filepath.Join(t.TempDir(), "server.json")
	after := logLine("02.500", "statement: SELECT 'another slice'")
	appendLog(t, log, []string{logLine("01.500", "statement: SELECT 'during'"), after[:len(after)/2]})

	past, err := holdsEnd(log, at("02.000")())
	if err != nil || past {
		t.Errorf("a cut line holds the end: %v %v", past, err)
	}

	appendLog(t, log, []string{after[len(after)/2:]})

	if past, err := holdsEnd(log, at("02.000")()); err != nil || !past {
		t.Errorf("the whole line doesn't hold the end: %v %v", past, err)
	}

	if past, err := holdsEnd(log, at("03.000")()); err != nil || past {
		t.Errorf("an earlier line holds a later end: %v %v", past, err)
	}
}

func TestASliceEndingBesideAnotherSharesItsLink(t *testing.T) {
	// the other slice links the file between this one's look and its link
	linkFile = func(old, link string) error {
		if err := os.Link(old, link); err != nil {
			return fmt.Errorf("the other slice's link: %w", err)
		}

		return &os.LinkError{Op: "link", Old: old, New: link, Err: syscall.EEXIST}
	}

	var said strings.Builder

	Log = &said

	t.Cleanup(func() { linkFile, Log = os.Link, os.Stderr })

	logDir, cov := t.TempDir(), t.TempDir()
	log := filepath.Join(logDir, "postgresql.json")
	appendLog(t, log, []string{logLine("00.500", "statement: SELECT 'before'")})

	cfg := Config{Log: logDir, Quiet: Duration(time.Millisecond)}
	dir := slicePart(cov, "api/a_test.go", 1)

	if err := Start(cfg, dir, at("01.000")); err != nil {
		t.Fatal(err)
	}

	appendLog(t, log, []string{logLine("01.500", "statement: SELECT 'during'")})

	if err := End(cfg, dir, "api/a_test.go", at("01.900")); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, LogDir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the part copied the log: %v", err)
	}

	if said.Len() > 0 {
		t.Errorf("said %q", said.String())
	}

	if got := own(t, cfg, dir); !reflect.DeepEqual(got, []string{"SELECT 'during'"}) {
		t.Errorf("got %q", got)
	}
}

func TestASlowEndSaysWhereItsTimeWent(t *testing.T) {
	var said strings.Builder

	Log, slowNote = &said, 0

	t.Cleanup(func() { Log, slowNote = os.Stderr, 2*time.Second })

	log := filepath.Join(t.TempDir(), "server.json")
	appendLog(t, log, []string{logLine("00.500", "statement: SELECT 'before'")})

	cfg := Config{Log: log, Quiet: Duration(time.Millisecond)}
	dir := t.TempDir()

	if err := Start(cfg, dir, at("01.000")); err != nil {
		t.Fatal(err)
	}

	appendLog(t, log, []string{logLine("01.500", "statement: SELECT 'during'")})

	if err := End(cfg, dir, "s", at("01.900")); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(said.String(), "waiting for the log to hold the window's end") {
		t.Errorf("said %q", said.String())
	}
}
