package gorecord

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func reset() {
	mu.Lock()
	defer mu.Unlock()

	if out != nil {
		_ = out.Close() //nolint:errcheck // a test's file
	}

	opened, out, dedup, failed = false, nil, false, false
	seen = map[string]bool{}
}

func lines(t *testing.T, name string) []map[string]any {
	t.Helper()

	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // read only

	var out []map[string]any

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("%s: %v", sc.Text(), err)
		}

		out = append(out, m)
	}

	return out
}

func TestNothingWithoutTheVariables(t *testing.T) {
	reset()
	t.Setenv(EnvDir, "")
	t.Setenv(EnvLog, "")

	Key("mail/locales", "de", "", "a")

	if out != nil {
		t.Error("a destination without a variable")
	}
}

func TestDirectoryModeWritesEachRecordOnce(t *testing.T) {
	reset()
	t.Cleanup(reset)

	dir := t.TempDir()
	t.Setenv(EnvDir, dir)
	t.Setenv(EnvLog, "")

	Load("mail/locales")
	Key("mail/locales", "de", "", "booking.subject")
	Key("mail/locales", "de", "", "booking.subject")
	Key("mail/locales", "de", "", "")
	Has("mail/locales", "de", "")
	Lang("mail/locales", "fr")

	files, err := filepath.Glob(filepath.Join(dir, "locale-keys.go-*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("files %v", files)
	}

	got := lines(t, files[0])
	if len(got) != 6 || got[0]["recorder"] != "locale-keys" || got[0]["runtime"] != "go" {
		t.Fatalf("%v", got)
	}

	// the whole namespace is the empty key, never no key
	if k, ok := got[3]["k"]; !ok || k != "" {
		t.Errorf("whole namespace: %v", got[3])
	}
}

func TestLogModeWritesEveryLookup(t *testing.T) {
	reset()
	t.Cleanup(reset)

	log := filepath.Join(t.TempDir(), "keys.log")
	t.Setenv(EnvDir, "")
	t.Setenv(EnvLog, log)

	Load("mail/locales")
	Key("mail/locales", "de", "", "a")
	Key("mail/locales", "de", "", "a")

	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}

	if n := strings.Count(string(b), `"k":"a"`); n != 2 {
		t.Errorf("%d lookups of a in the log, want 2:\n%s", n, b)
	}
}
