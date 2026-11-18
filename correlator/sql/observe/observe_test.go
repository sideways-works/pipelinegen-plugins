package observe

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen-plugins/correlator/sql/collect"
)

func slice(t *testing.T, statements string) string {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, collect.StatementsFile), []byte(statements), 0o600); err != nil {
		t.Fatal(err)
	}

	return dir
}

func TestTouched(t *testing.T) {
	dir := slice(t, `{"text":"SELECT n.title FROM notes n JOIN app.users u ON u.id = n.author","count":3}
{"text":"INSERT INTO note_events (note_id) VALUES ($1)","count":1}
{"text":"INSERT INTO tags (name) VALUES ($1)","count":1,"failed":true}
{"text":"SELECT note_score($1), count(*), now() FROM pg_class","count":1}
`)

	obs, err := Touched(Config{SearchPath: []string{"app", "public"}}, []string{noSetup(t), dir}, "")
	if err != nil {
		t.Fatal(err)
	}

	got := obs.Touched

	want := []string{
		"pg:data:app.notes",
		"pg:data:app.users",
		"pg:data:public.notes",
		"pg:relation:app.note_events",
		"pg:relation:app.notes",
		"pg:relation:app.tags",
		"pg:relation:app.users",
		"pg:relation:public.note_events",
		"pg:relation:public.notes",
		"pg:relation:public.tags",
		"pg:routine:app.note_score",
		"pg:routine:public.note_score",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
}

func TestTouchedDefaultsToPublic(t *testing.T) {
	obs, err := Touched(Config{}, []string{noSetup(t), slice(t, `{"text":"DELETE FROM notes WHERE id = $1","count":1}`+"\n")}, "")
	if err != nil {
		t.Fatal(err)
	}

	got := obs.Touched
	if want := []string{"pg:data:public.notes", "pg:relation:public.notes"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
}

func TestTouchedCannotTell(t *testing.T) {
	_, err := Touched(Config{}, []string{noSetup(t), slice(t, `{"text":"what ( is this","count":1}`+"\n")}, "")
	if !errors.Is(err, ErrCannotTell) {
		t.Fatalf("got %v", err)
	}
}

func TestAMissingSliceCannotTell(t *testing.T) {
	_, err := Touched(Config{}, []string{noSetup(t), filepath.Join(t.TempDir(), "gone")}, "")
	if !errors.Is(err, ErrCannotTell) {
		t.Fatalf("got %v", err)
	}
}

func TestASliceDirectoryWithoutStatementsCannotTell(t *testing.T) {
	if _, err := Touched(Config{}, []string{noSetup(t), t.TempDir()}, ""); !errors.Is(err, ErrCannotTell) {
		t.Fatalf("got %v", err)
	}
}

func TestWithoutALogCannotTell(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, collect.UnconfiguredFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Touched(Config{}, []string{noSetup(t), dir}, ""); !errors.Is(err, ErrCannotTell) {
		t.Fatalf("got %v", err)
	}
}

func TestTouchedNothing(t *testing.T) {
	obs, err := Touched(Config{}, []string{noSetup(t), slice(t, "")}, "")
	if err != nil || len(obs.Touched) != 0 {
		t.Fatalf("got %+v, %v", obs, err)
	}
}

func TestTouchedUnreadableStatements(t *testing.T) {
	_, err := Touched(Config{}, []string{noSetup(t), slice(t, "not json\n")}, "")
	if err == nil || errors.Is(err, ErrCannotTell) {
		t.Fatalf("got %v, want a failure", err)
	}
}

func TestTouchedThroughTheCache(t *testing.T) {
	cache := t.TempDir()
	dir := slice(t, `{"text":"SELECT title FROM notes","count":1}`+"\n")

	first, err := Touched(Config{}, []string{noSetup(t), dir}, cache)
	if err != nil {
		t.Fatal(err)
	}

	cached, err := filepath.Glob(filepath.Join(cache, "statements-v1", "*", "*.json"))
	if err != nil || len(cached) != 1 {
		t.Fatalf("cache %v %v", cached, err)
	}

	second, err := Touched(Config{}, []string{noSetup(t), dir}, cache)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(first, second) || len(first.Touched) == 0 {
		t.Fatalf("%q then %q", first, second)
	}
}

func TestSetupApart(t *testing.T) {
	root := t.TempDir()
	setup := filepath.Join(root, "coverage", "_setup", "sql")
	own := filepath.Join(root, "coverage", "api", "notes_test.go", "sql")

	for dir, files := range map[string]map[string]string{
		setup: {collect.StatementsFile: `{"text":"INSERT INTO users (name) VALUES ($1)","count":1}` + "\n"},
		own: {
			collect.StatementsFile: `{"text":"SELECT notes.title FROM notes JOIN users ON users.id = notes.author","count":1}` + "\n",
			collect.SetupFile:      `{"text":"ALTER TABLE tags ADD COLUMN color text","count":1}` + "\n",
		},
	} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}

		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	obs, err := Touched(Config{}, []string{setup, own}, "")
	if err != nil {
		t.Fatal(err)
	}

	// users is the slice's own too, since the slice read it
	if want := []string{"pg:data:public.tags", "pg:relation:public.tags"}; !reflect.DeepEqual(obs.SetupLike, want) {
		t.Errorf("setup-like %q", obs.SetupLike)
	}

	if want := []string{"pg:data:public.notes", "pg:data:public.tags", "pg:data:public.users", "pg:relation:public.notes", "pg:relation:public.tags", "pg:relation:public.users"}; !reflect.DeepEqual(obs.Touched, want) {
		t.Errorf("touched %q", obs.Touched)
	}

	// a batch without a setup directory is fine; a slice without its own is not
	if _, err := Touched(Config{}, []string{filepath.Join(root, "none", "_setup", "sql"), own}, ""); err != nil {
		t.Errorf("no setup: %v", err)
	}
}

func noSetup(t *testing.T) string {
	t.Helper()

	return filepath.Join(t.TempDir(), "setup")
}

func TestSetupByPlace(t *testing.T) {
	setup := slice(t, `{"text":"INSERT INTO users (name) VALUES ($1)","count":1}`+"\n")
	part := filepath.Join(t.TempDir(), "_setup")

	if err := os.MkdirAll(part, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(part, collect.StatementsFile), []byte(`{"text":"SELECT 1 FROM notes","count":1}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	obs, err := Touched(Config{}, []string{setup, part}, "")
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"pg:relation:public.users"}; !reflect.DeepEqual(obs.SetupLike, want) {
		t.Errorf("setup-like %q, want what dirs[0] ran", obs.SetupLike)
	}

	if _, err := Touched(Config{}, []string{"sql", part}, ""); err != nil {
		t.Errorf("a missing setup by another name: %v", err)
	}
}

func TestNoPartCannotTell(t *testing.T) {
	for name, dirs := range map[string][]string{
		"setup only":   {slice(t, `{"text":"SELECT 1 FROM notes","count":1}`+"\n")},
		"no directory": nil,
	} {
		if _, err := Touched(Config{}, dirs, ""); !errors.Is(err, ErrCannotTell) {
			t.Errorf("%s: %v, want can't tell", name, err)
		}
	}
}

func TestAnUnreadableLogCannotTell(t *testing.T) {
	logDir, dir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(logDir, "postgresql.json"), []byte(`al_count": 3}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := collect.Config{Log: logDir, Quiet: collect.Duration(time.Millisecond)}
	if err := collect.Setup(cfg, dir, time.Now); err != nil {
		t.Fatal(err)
	}

	for _, dirs := range [][]string{{noSetup(t), dir}, {dir, slice(t, "")}} {
		_, err := Touched(Config{Collect: cfg}, dirs, "")
		if !errors.Is(err, ErrCannotTell) || !strings.Contains(err.Error(), "postgresql.json: line 1 (byte 0):") {
			t.Errorf("dirs %v: %v", dirs, err)
		}
	}
}

func TestAMarkedPartCannotTell(t *testing.T) {
	why := "slice-start came after the slice had already run: tests.coverprofile"

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, collect.UnboundedFile), []byte(why+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Touched(Config{}, []string{noSetup(t), dir}, "")
	if !errors.Is(err, ErrCannotTell) || !strings.Contains(err.Error(), why) {
		t.Errorf("%v", err)
	}
}
