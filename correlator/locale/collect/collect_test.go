package collect

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen-sbom-locale/catalog"

	"github.com/sideways-works/pipelinegen-correlator-locale/observe"
)

var cats = observe.NewCatalogs([]string{"mail/locales/en.json", "mail/locales/de.json"})

func appendLog(t *testing.T, log string, lines ...string) {
	t.Helper()

	f, err := os.OpenFile(log, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	defer f.Close() //nolint:errcheck // written below

	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		t.Fatal(err)
	}
}

func TestTakeMovesTheRecordersFiles(t *testing.T) {
	d := t.TempDir()
	slice := filepath.Join(d, "coverage", "s")
	own := filepath.Join(slice, "locale")

	if err := os.MkdirAll(slice, 0o750); err != nil {
		t.Fatal(err)
	}

	for n, text := range map[string]string{
		"locale-keys.go-1-1.jsonl": `{"recorder":"locale-keys","version":1,"runtime":"go"}` + "\n" + `{"c":"mail/locales","l":"de","n":"","k":"a"}` + "\n",
		"tests.coverprofile":       "mode: set\n",
	} {
		if err := os.WriteFile(filepath.Join(slice, n), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := Take(slice, own); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(slice, "tests.coverprofile")); err != nil {
		t.Error("another plugin's file was moved")
	}

	ans, err := observe.Observe([]string{filepath.Join(d, "setup"), own}, cats)
	if err != nil || !slices.Contains(ans.Touched, catalog.KeyID("mail/locales", "de", "-", "a")) {
		t.Errorf("%+v %v", ans, err)
	}
}

func TestCutPerSlice(t *testing.T) {
	d := t.TempDir()
	log := filepath.Join(d, "locale-keys.log")
	setup, s1, s2 := filepath.Join(d, "_setup", "locale"), filepath.Join(d, "s1", "locale"), filepath.Join(d, "s2", "locale")

	appendLog(t, log, `{"recorder":"locale-keys","version":1,"runtime":"go","pid":9}`, `{"c":"mail/locales","load":true}`)

	if err := Cut(log, setup, true); err != nil {
		t.Fatal(err)
	}

	for _, s := range []string{s1, s2} {
		if err := Mark(log, s); err != nil {
			t.Fatal(err)
		}

		appendLog(t, log, `{"c":"mail/locales","l":"de","n":"","k":"booking.subject"}`)

		if s == s1 {
			appendLog(t, log, `{"c":"mail/locales","l":"de","n":"","k":"only.first"}`)
		}

		if err := Cut(log, s, false); err != nil {
			t.Fatal(err)
		}

		if err := Take(filepath.Dir(s), s); err != nil {
			t.Fatal(err)
		}
	}

	a1, err := observe.Observe([]string{setup, s1}, cats)
	if err != nil {
		t.Fatal(err)
	}

	a2, err := observe.Observe([]string{setup, s2}, cats)
	if err != nil {
		t.Fatal(err)
	}

	first := catalog.KeyID("mail/locales", "de", "-", "only.first")
	subject := catalog.KeyID("mail/locales", "de", "-", "booking.subject")

	if !slices.Contains(a1.Touched, first) || !slices.Contains(a1.Touched, subject) {
		t.Errorf("s1 %v", a1.Touched)
	}

	if slices.Contains(a2.Touched, first) || !slices.Contains(a2.Touched, subject) || !slices.Contains(a2.Touched, "locale:mail/locales") {
		t.Errorf("s2 %v", a2.Touched)
	}
}

func TestStackWithoutRecorder(t *testing.T) {
	d := t.TempDir()
	log := filepath.Join(d, "locale-keys.log")
	setup, s := filepath.Join(d, "_setup", "locale"), filepath.Join(d, "s", "locale")

	if err := Cut(log, setup, true); !errors.Is(err, ErrNoRecorder) {
		t.Fatalf("got %v", err)
	}

	if err := Mark(log, s); err != nil {
		t.Fatal(err)
	}

	if err := Cut(log, s, false); err != nil {
		t.Fatal(err)
	}

	ans, err := observe.Observe([]string{setup, s}, cats)
	if err != nil || len(ans.Touched) != 0 {
		t.Errorf("%+v %v", ans, err)
	}
}

func TestSliceDir(t *testing.T) {
	for own, want := range map[string]string{
		"cov/e2e/a.spec.ts/locale":                         "cov/e2e/a.spec.ts",
		"cov/.pipelinegen/part-2/e2e/a.spec.ts/locale":     "cov/e2e/a.spec.ts",
		"cov/.pipelinegen/part-13/e2e/a.spec.ts/locale":    "cov/e2e/a.spec.ts",
		"cov/_setup/locale":                                "cov/_setup",
		"cov/.pipelinegen/part-1/e2e/a.spec.ts/locale":     "cov/.pipelinegen/part-1/e2e/a.spec.ts",
		"cov/other/.pipelinegen/part-2/x/a.spec.ts/locale": "cov/other/.pipelinegen/part-2/x/a.spec.ts",
	} {
		slice := "e2e/a.spec.ts"
		if strings.HasPrefix(own, "cov/_setup") {
			slice = "_setup"
		}

		if got := SliceDir(own, slice); got != want {
			t.Errorf("SliceDir(%q) = %q, want %q", own, got, want)
		}
	}
}

func TestCutWithoutMarkFails(t *testing.T) {
	d := t.TempDir()
	if err := Cut(filepath.Join(d, "log"), filepath.Join(d, "s"), false); !errors.Is(err, ErrCollect) {
		t.Errorf("got %v", err)
	}
}

func TestCutKeepsWholeLines(t *testing.T) {
	d := t.TempDir()
	log := filepath.Join(d, "log")
	s := filepath.Join(d, "s")

	appendLog(t, log, `{"recorder":"locale-keys","version":1,"runtime":"go"}`)

	if err := Mark(log, s); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(log, append(must(os.ReadFile(log)), []byte(`{"c":"mail/loc`)...), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Cut(log, s, false); err != nil {
		t.Fatal(err)
	}

	b := must(os.ReadFile(filepath.Join(s, "locale-keys.log.jsonl")))
	if strings.Contains(string(b), "mail/loc") {
		t.Errorf("a half line was cut: %s", b)
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}

	return b
}
