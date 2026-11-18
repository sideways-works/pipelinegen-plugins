package correlate

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func observeLog(t *testing.T, m *Model, lines string) ([]string, string, error) {
	t.Helper()

	part := t.TempDir()
	log := filepath.Join(part, RequestsFile)
	write(t, log, lines)

	got, err := m.Observe([]string{filepath.Join(t.TempDir(), "missing"), part})

	return got, log, err
}

func TestURLLinesMatchingNothingAreOrdinary(t *testing.T) {
	m := load(t)
	m.Config.RequestLogLines = LinesURLs

	got, _, err := observeLog(t, m, "GET /api/notes/n1\nGET /healthz\nGET /api/old/7\n")
	if err != nil || !slices.Equal(got, []string{"api:getNote"}) {
		t.Errorf("got %q, %v; want getNote alone", got, err)
	}
}

func TestPatternLines(t *testing.T) {
	m := load(t)
	m.Config.RequestLogLines = LinesPatterns
	m.Config.OtherRoutes = []string{"GET /healthz", "GET /api/admin/{path...}"}

	t.Run("a pattern touches its operation, whatever its parameters are called", func(t *testing.T) {
		got, _, err := observeLog(t, m, "GET /api/notes/{id}\nPOST /api/notes\nGET /api/notes/{noteID}.txt\nHEAD /api/notes/drafts\n")
		want := []string{"api:createNote", "api:getNote", "api:getNoteText", "api:list-drafts"}

		if err != nil || !slices.Equal(got, want) {
			t.Errorf("got %q, %v; want %q", got, err, want)
		}
	})

	t.Run("a listed other route touches nothing", func(t *testing.T) {
		got, _, err := observeLog(t, m, "GET /healthz\nGET /api/admin/{rest...}\nGET /api/notes\n")
		if err != nil || !slices.Equal(got, []string{"api:listNotes"}) {
			t.Errorf("got %q, %v; want listNotes alone", got, err)
		}
	})

	t.Run("a pattern no contract has can't tell, naming it and the log", func(t *testing.T) {
		got, log, err := observeLog(t, m, "GET /api/notes\nGET /api/old/{id}\n")
		if !errors.Is(err, ErrUnknownRoute) {
			t.Fatalf("got %q, %v; want can't tell", got, err)
		}

		for _, part := range []string{"GET /api/old/{id}", log, "other_routes"} {
			if !strings.Contains(err.Error(), part) {
				t.Errorf("%q doesn't name %q", err, part)
			}
		}
	})

	t.Run("a URL in a log of patterns says the log seems to hold URLs", func(t *testing.T) {
		got, _, err := observeLog(t, m, "GET /api/notes/n1\n")
		if !errors.Is(err, ErrUnknownRoute) {
			t.Fatalf("got %q, %v; want can't tell", got, err)
		}

		if !strings.Contains(err.Error(), "seems to hold URLs") || !strings.Contains(err.Error(), "request_log_lines") {
			t.Errorf("%q doesn't point at the setting", err)
		}
	})

	t.Run("an unknown pattern in the setup can't tell too", func(t *testing.T) {
		setup, part := t.TempDir(), t.TempDir()
		write(t, filepath.Join(setup, RequestsFile), "GET /api/gone\n")
		write(t, filepath.Join(part, RequestsFile), "GET /api/notes\n")

		if got, err := m.Observe([]string{setup, part}); !errors.Is(err, ErrUnknownRoute) {
			t.Errorf("got %q, %v; want can't tell", got, err)
		}
	})

	t.Run("a HAR holds URLs whatever the log holds", func(t *testing.T) {
		part := t.TempDir()
		write(t, filepath.Join(part, "page.har"), `{"log":{"entries":[
			{"request":{"method":"GET","url":"https://notes.example/api/notes/n1"}},
			{"request":{"method":"GET","url":"https://notes.example/favicon.ico"}}]}}`)

		got, err := m.Observe([]string{t.TempDir(), part})
		if err != nil || !slices.Equal(got, []string{"api:getNote"}) {
			t.Errorf("got %q, %v; want getNote alone", got, err)
		}
	})
}

func TestRequestLogLinesUnknownValue(t *testing.T) {
	m := load(t)
	m.Config.RequestLogLines = "paths"

	if _, _, err := observeLog(t, m, "GET /api/notes\n"); err == nil || errors.Is(err, ErrUnknownRoute) || errors.Is(err, ErrNoData) {
		t.Errorf("got %v; want a config error", err)
	}
}
