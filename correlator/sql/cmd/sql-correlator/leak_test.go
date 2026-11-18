package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// marker stands for a value a test used, a role's password among them:
// it must never leave the log it was written into.
const marker = "pw-7f0a3c-made-up-secret"

func logLine(ts string, fields map[string]any) string {
	if ts == "" {
		ts = time.Now().UTC().Format("2006-01-02 15:04:05.000") + " UTC"
	}

	rec := map[string]any{"timestamp": ts, "dbname": "app_test", "user": "app", "error_severity": "LOG"}
	maps.Copy(rec, fields)

	b, err := json.Marshal(rec)
	if err != nil {
		panic(err)
	}

	return string(b) + "\n"
}

func appendTo(t *testing.T, name, text string) {
	t.Helper()

	f, err := os.OpenFile(name, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}

	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestNoValueLeavesTheLog: however a part fails, a made-up secret in the log
// reaches no answer, no message and no file outside the log's link or copy.
func TestNoValueLeavesTheLog(t *testing.T) {
	for name, c := range map[string]struct {
		// before runs ahead of slice-start, during between the start and
		// the end, after between the end and observe
		before, during, after func(t *testing.T, log, part string)
		want                  string
	}{
		"a statement that doesn't parse": {
			during: func(t *testing.T, log, _ string) {
				t.Helper()

				appendTo(t, log, logLine("", map[string]any{"message": "statement: ALTER ROLE app PASSWORD '" + marker + "' SELEC"}))
			},
			want: "statements don't parse",
		},
		"a line that isn't JSON": {
			during: func(t *testing.T, log, _ string) {
				t.Helper()

				appendTo(t, log, marker+" LOG: statement: SELECT 1\n")
			},
			want: "JSON syntax error at byte",
		},
		"a field of another type": {
			during: func(t *testing.T, log, _ string) {
				t.Helper()

				appendTo(t, log, `{"timestamp": "`+time.Now().UTC().Format("2006-01-02 15:04:05.000")+` UTC", "message": 7, "detail": "`+marker+`"}`+"\n")
			},
			want: "the field message holds a JSON number",
		},
		"a time zone other than UTC": {
			during: func(t *testing.T, log, _ string) {
				t.Helper()

				appendTo(t, log, logLine("2026-01-05 10:00:01.100 "+marker, map[string]any{"message": "statement: SELECT 1"}))
			},
			want: "log in UTC",
		},
		"a timestamp PostgreSQL doesn't write": {
			during: func(t *testing.T, log, _ string) {
				t.Helper()

				appendTo(t, log, logLine(marker+" UTC", map[string]any{"message": "statement: SELECT 1"}))
			},
			want: "a timestamp PostgreSQL doesn't write",
		},
		"a server clock ahead": {
			during: func(t *testing.T, log, _ string) {
				t.Helper()

				appendTo(t, log, logLine("2099-01-05 10:00:01.100 UTC", map[string]any{"message": "statement: SELECT '" + marker + "'"}))
			},
			want: "later than this machine's clock",
		},
		"bytes written over before observe": {
			during: func(t *testing.T, log, _ string) {
				t.Helper()

				appendTo(t, log, logLine("", map[string]any{"message": "statement: SELECT 1"}))
			},
			after: func(t *testing.T, log, _ string) {
				t.Helper()

				if err := os.Truncate(log, 0); err != nil {
					t.Fatal(err)
				}

				appendTo(t, log, strings.Repeat(logLine("", map[string]any{"message": "statement: SELECT '" + marker + "'"}), 3))
			},
			want: "no longer holds at byte",
		},
		"a slice that ran before its start": {
			before: func(t *testing.T, _, part string) {
				t.Helper()

				if err := os.MkdirAll(part, 0o750); err != nil {
					t.Fatal(err)
				}

				if err := os.WriteFile(filepath.Join(part, "tests.coverprofile"), []byte("mode: atomic\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			during: func(t *testing.T, log, _ string) {
				t.Helper()

				appendTo(t, log, logLine("", map[string]any{"message": "statement: SELECT '" + marker + "'"}))
			},
			want: "slice-start came after the slice had already run",
		},
		"a missing part": {
			during: func(t *testing.T, log, _ string) {
				t.Helper()

				appendTo(t, log, logLine("", map[string]any{"message": "statement: SELECT '" + marker + "'"}))
			},
			after: func(t *testing.T, _, part string) {
				t.Helper()

				if err := os.RemoveAll(part); err != nil {
					t.Fatal(err)
				}
			},
			want: "is missing",
		},
		"a part that reads": {
			during: func(t *testing.T, log, _ string) {
				t.Helper()

				appendTo(t, log, logLine("", map[string]any{"message": "statement: SELECT id FROM notes WHERE token = '" + marker + "'"}))
				appendTo(t, log, logLine("", map[string]any{
					"error_severity": "ERROR", "message": "duplicate key value violates unique constraint",
					"detail": "Key (token)=(" + marker + ") already exists.", "statement": "INSERT INTO tags (name) VALUES ('" + marker + "')",
				}))
			},
			want: `"pg:relation:public.notes"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			logDir, cov := t.TempDir(), t.TempDir()
			log := filepath.Join(logDir, "postgresql.json")
			appendTo(t, log, "")

			cfg := `{"log":"` + logDir + `","quiet":"10ms"}`
			setup, part := filepath.Join(cov, "_setup", "sql"), filepath.Join(cov, "s", "sql")
			slicePart := filepath.Dir(part)

			var said strings.Builder

			collect := func(action, slice, dir string) {
				_, out, errs := call(t, "collect", `{"protocol":1,"config":`+cfg+`,"action":"`+action+`","slice":"`+slice+`","dir":"`+dir+`"}`)
				said.WriteString(out + errs)
			}

			collect("setup-end", "", setup)

			if c.before != nil {
				c.before(t, log, slicePart)
			}

			collect("slice-start", "s", part)

			if c.during != nil {
				c.during(t, log, part)
			}

			collect("slice-end", "s", part)

			if c.after != nil {
				c.after(t, log, slicePart)
			}

			_, out, errs := call(t, "observe", `{"protocol":1,"config":`+cfg+`,"slice":"s","dirs":["`+setup+`","`+part+`"]}`)
			said.WriteString(out + errs)

			if !strings.Contains(said.String(), c.want) {
				t.Errorf("the run doesn't say %q:\n%s", c.want, said.String())
			}

			if strings.Contains(said.String(), marker) {
				t.Errorf("the secret left the log in what was said:\n%s", said.String())
			}

			// every file written besides the log's own link or copy
			err := filepath.WalkDir(cov, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}

				if strings.Contains(d.Name(), marker) {
					t.Errorf("a file is named with the secret: %s", p)
				}

				if d.IsDir() && d.Name() == "log" {
					return filepath.SkipDir
				}

				if d.IsDir() {
					return nil
				}

				data, err := os.ReadFile(p) //nolint:gosec // the test's own coverage directory
				if err != nil {
					return fmt.Errorf("reading %s: %w", p, err)
				}

				if strings.Contains(string(data), marker) {
					t.Errorf("%s holds the secret", p)
				}

				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
