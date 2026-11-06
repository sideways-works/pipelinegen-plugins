package observe

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	goHead     = `{"recorder":"locale-keys","version":1,"runtime":"go","pid":7}`
	vitestHead = `{"recorder":"locale-keys","version":1,"runtime":"vitest","pid":7}`
	end        = `{"end":true}`
)

var claimed = []string{
	"web/locales/en/home.json", "web/locales/de/home.json",
	"mail/locales/en.json", "mail/locales/de.json",
}

func part(t *testing.T, dir, name string, lines ...string) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}

	text := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func observe(t *testing.T, dirs ...string) (Answer, error) {
	t.Helper()

	return Observe(dirs, NewCatalogs(claimed))
}

func TestGoLookupsBecomeUnits(t *testing.T) {
	d := t.TempDir()
	part(t, filepath.Join(d, "s"), "locale-keys.go-7-1.jsonl", goHead,
		`{"c":"mail/locales","load":true}`,
		`{"c":"mail/locales","l":"de","lang":true}`,
		`{"c":"mail/locales","l":"de","n":"","k":"booking.subject"}`,
		`{"c":"mail/locales","l":"fr","n":"","k":"booking"}`,
		`{"c":"mail/locales","l":"de","n":"","k":""}`)

	ans, err := observe(t, filepath.Join(d, "setup"), filepath.Join(d, "s"))
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"locale:mail/locales",
		"locale:mail/locales#de",
		"locale:mail/locales#de/-:",
		"locale:mail/locales#de/-:booking.subject",
		"locale:mail/locales#fr/-:booking",
	}
	if !slices.Equal(ans.Touched, want) {
		t.Errorf("touched %v, want %v", ans.Touched, want)
	}
}

func TestFileRecordsNameTheFilesNamespace(t *testing.T) {
	d := t.TempDir()
	part(t, filepath.Join(d, "s", "1"), "locale-keys.vitest-7-1.jsonl", vitestHead,
		`{"f":"web/locales/de/home.json","k":"title"}`,
		`{"f":"web/locales/de/home.json","has":true}`,
		// a JSON file no claim names narrows nothing
		`{"f":"web/fixtures/en/home.json","k":"title"}`,
		end)

	ans, err := observe(t, filepath.Join(d, "setup"), filepath.Join(d, "s"))
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"locale:web/locales", "locale:web/locales#de/home", "locale:web/locales#de/home:title"}
	if !slices.Equal(ans.Touched, want) {
		t.Errorf("touched %v, want %v", ans.Touched, want)
	}
}

func TestSetupIsApart(t *testing.T) {
	d := t.TempDir()
	part(t, filepath.Join(d, "setup"), "locale-keys.go-7-1.jsonl", goHead,
		`{"c":"mail/locales","load":true}`,
		`{"c":"mail/locales","l":"en","n":"","k":"boot.subject"}`)
	part(t, filepath.Join(d, "s"), "locale-keys.collect.jsonl", `{"recorder":"locale-keys","version":1,"runtime":"collect"}`, end)

	ans, err := observe(t, filepath.Join(d, "setup"), filepath.Join(d, "s"))
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(ans.SetupLike, []string{"locale:mail/locales#en/-:boot.subject"}) ||
		!slices.Contains(ans.Touched, "locale:mail/locales") || !slices.Contains(ans.Touched, "locale:mail/locales#en/-:boot.subject") {
		t.Errorf("%+v", ans)
	}
}

func TestNothingLookedUp(t *testing.T) {
	d := t.TempDir()
	part(t, filepath.Join(d, "s"), "locale-keys.collect.jsonl", `{"recorder":"locale-keys","version":1,"runtime":"collect"}`, end)

	ans, err := observe(t, filepath.Join(d, "nosetup"), filepath.Join(d, "s"))
	if err != nil || len(ans.Touched) != 0 {
		t.Errorf("%+v, %v", ans, err)
	}
}

func TestCantTell(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string){
		"no part": func(*testing.T, string) {},
		"an empty directory": func(t *testing.T, dir string) {
			t.Helper()

			if err := os.MkdirAll(dir, 0o750); err != nil {
				t.Fatal(err)
			}
		},
		"no recorder's header": func(t *testing.T, dir string) {
			t.Helper()
			part(t, dir, "locale-keys.go-1-1.jsonl", `{"c":"mail/locales","load":true}`)
		},
		"another version": func(t *testing.T, dir string) {
			t.Helper()
			part(t, dir, "locale-keys.go-1-1.jsonl", `{"recorder":"locale-keys","version":2,"runtime":"go"}`)
		},
		"a part cut short": func(t *testing.T, dir string) {
			t.Helper()

			if err := os.MkdirAll(dir, 0o750); err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(filepath.Join(dir, "locale-keys.go-1-1.jsonl"), []byte(goHead+"\n"+`{"c":"mail/loc`), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"a JavaScript part without its end": func(t *testing.T, dir string) {
			t.Helper()
			part(t, dir, "locale-keys.vitest-1-1.jsonl", vitestHead, `{"f":"web/locales/de/home.json","k":"title"}`)
		},
		"the recorder couldn't tell": func(t *testing.T, dir string) {
			t.Helper()
			part(t, dir, "locale-keys.browser-1-1.jsonl", `{"recorder":"locale-keys","version":1,"runtime":"browser"}`,
				`{"cant":"the page http://app/ loaded the app without the recorder"}`, end)
		},
		"a catalog no claim names": func(t *testing.T, dir string) {
			t.Helper()
			part(t, dir, "locale-keys.go-1-1.jsonl", goHead, `{"c":"mail/locale","load":true}`)
		},
		"a catalog outside the tree": func(t *testing.T, dir string) {
			t.Helper()
			part(t, dir, "locale-keys.go-1-1.jsonl", goHead, `{"c":"../mail/locales","load":true}`)
		},
	}

	for name, write := range cases {
		t.Run(name, func(t *testing.T) {
			d := t.TempDir()
			write(t, filepath.Join(d, "s"))

			_, err := observe(t, filepath.Join(d, "setup"), filepath.Join(d, "s"))
			if !errors.Is(err, ErrCantTell) {
				t.Errorf("got %v, want can't tell", err)
			}
		})
	}
}

func TestSetupCantTellMarksTheSlice(t *testing.T) {
	d := t.TempDir()
	part(t, filepath.Join(d, "setup"), "locale-keys.log.jsonl", `{"recorder":"locale-keys","version":1,"runtime":"collect"}`,
		`{"cant":"no process of the stack records"}`, end)
	part(t, filepath.Join(d, "s"), "locale-keys.collect.jsonl", `{"recorder":"locale-keys","version":1,"runtime":"collect"}`, end)

	if _, err := observe(t, filepath.Join(d, "setup"), filepath.Join(d, "s")); !errors.Is(err, ErrCantTell) {
		t.Errorf("got %v", err)
	}
}

func TestUnreadable(t *testing.T) {
	d := t.TempDir()
	part(t, filepath.Join(d, "s"), "locale-keys.go-1-1.jsonl", goHead, `not json`)

	if _, err := observe(t, filepath.Join(d, "setup"), filepath.Join(d, "s")); !errors.Is(err, ErrUnreadable) {
		t.Errorf("got %v", err)
	}
}

func TestPartsAddUp(t *testing.T) {
	d := t.TempDir()
	part(t, filepath.Join(d, "s"), "locale-keys.go-1-1.jsonl", goHead, `{"c":"mail/locales","l":"de","n":"","k":"a"}`)
	part(t, filepath.Join(d, "p2"), "locale-keys.go-2-1.jsonl", goHead, `{"c":"mail/locales","l":"de","n":"","k":"b"}`)
	part(t, filepath.Join(d, "s", "3"), "locale-keys.vitest-1-1.jsonl", vitestHead, `{"f":"web/locales/en/home.json","k":"c"}`, end)

	ans, err := observe(t, filepath.Join(d, "setup"), filepath.Join(d, "s"), filepath.Join(d, "p2"))
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"locale:mail/locales#de/-:a", "locale:mail/locales#de/-:b", "locale:web/locales#en/home:c"} {
		if !slices.Contains(ans.Touched, id) {
			t.Errorf("no %s in %v", id, ans.Touched)
		}
	}
}

func TestForeignTreeCantTell(t *testing.T) {
	d := t.TempDir()
	part(t, filepath.Join(d, "s"), "locale-keys.go-1-1.jsonl", goHead, `{"c":"mail/locales","l":"de","n":"","k":"a"}`)

	_, err := Observe([]string{filepath.Join(d, "setup"), filepath.Join(d, "s")}, NewCatalogs([]string{"other/locales/en.json"}))
	if !errors.Is(err, ErrCantTell) {
		t.Errorf("got %v", err)
	}
}

func TestSymlinkedPart(t *testing.T) {
	d := t.TempDir()
	part(t, filepath.Join(d, "real"), "locale-keys.go-1-1.jsonl", goHead, `{"c":"mail/locales","l":"de","n":"","k":"a"}`)

	if err := os.Symlink(filepath.Join(d, "real"), filepath.Join(d, "link")); err != nil {
		t.Fatal(err)
	}

	ans, err := observe(t, filepath.Join(d, "setup"), filepath.Join(d, "link"))
	if err != nil || !slices.Contains(ans.Touched, "locale:mail/locales#de/-:a") {
		t.Errorf("%+v %v", ans, err)
	}
}
