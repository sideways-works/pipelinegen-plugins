package driver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAProfileKeepsWhatRanAndEveryFile(t *testing.T) {
	in := `mode: atomic
example.com/m/a/a.go:5.30,5.48 1 3
example.com/m/a/a.go:8.30,8.48 1 0
example.com/m/a/a.go:11.25,11.36 1 1
example.com/m/b/b.go:3.20,5.2 2 0
example.com/m/b/b.go:7.20,9.2 2 0
other.org/x/x.go:3.1,4.2 1 0
`
	want := `mode: atomic
example.com/m/a/a.go:5.30,5.48 1 3
example.com/m/a/a.go:11.25,11.36 1 1
example.com/m/b/b.go:3.20,5.2 2 0
other.org/x/x.go:3.1,4.2 1 0
`

	p := filepath.Join(t.TempDir(), "tests.coverprofile")
	if err := os.WriteFile(p, []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := cutProfile(p); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}

	odd := in + "not a block line\n"
	if err := os.WriteFile(p, []byte(odd), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := cutProfile(p); err != nil {
		t.Fatal(err)
	}

	if got, _ := os.ReadFile(p); string(got) != odd { //nolint:errcheck // compared below
		t.Errorf("a profile with a line it can't read was changed:\n%s", got)
	}
}

func TestARecordingKeepsOnlyWhatRan(t *testing.T) {
	for _, batch := range []bool{false, true} {
		tree := fixture(t)
		runFixture(t, tree, batch, true, nil)

		add := profile(t, filepath.Join(tree+"-rec", "calc/add_test.go"))

		if count(add, "calc/calc.go:5.") == "0" || count(add, "calc/calc.go:8.") != "none" {
			t.Errorf("batch %v: add's recording keeps Mul's zero block:\n%s", batch, strings.Join(add, "\n"))
		}
	}
}
