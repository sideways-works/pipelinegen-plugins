package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The kit fixture's copy must match too, so the fixture tests what ships.
func TestMarksWritesTheRepositorysFile(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("..", "..", "marks.mjs"))
	if err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(t.TempDir(), "fixture", "e2e")

	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), []string{"marks", dir}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}

	path := filepath.Join(dir, "marks.mjs")
	if got := strings.TrimSpace(stdout.String()); got != path {
		t.Errorf("printed %q, want %q", got, path)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, want) {
		t.Errorf("the written marks.mjs (%d bytes) is not the repository's (%d bytes)", len(got), len(want))
	}

	fixture, err := os.ReadFile(filepath.Join("..", "..", "driver", "testdata", "web", "e2e", "marks.mjs"))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(fixture, want) {
		t.Error("driver/testdata/web/e2e/marks.mjs is not the repository's marks.mjs: copy it again")
	}
}

func TestMarksWritesOverAnOlderFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marks.mjs"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), []string{"marks", dir}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}

	got, err := os.ReadFile(filepath.Join(dir, "marks.mjs"))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Contains(got, []byte("markStart")) {
		t.Errorf("marks.mjs still holds %q", got)
	}
}

func TestMarksNeedsOneDirectory(t *testing.T) {
	for _, args := range [][]string{{"marks"}, {"marks", "a", "b"}} {
		var stdout, stderr bytes.Buffer
		if code := run(t.Context(), args, nil, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "usage: playwright-driver marks <dir>") {
			t.Errorf("%v: exit %d, %q", args, code, stderr.String())
		}
	}
}
