// Package recorder holds the JavaScript recorders the binary writes out with
// `locale-correlator recorder <dir>`, so a release stays one file.
package recorder

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed js/*.mjs
var files embed.FS

// Names are the files Write writes.
func Names() []string {
	entries, _ := fs.ReadDir(files, "js") //nolint:errcheck // an embedded directory always reads

	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}

	return out
}

// Write writes the recorders into dir, replacing what is there.
func Write(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("writing the recorders: %w", err)
	}

	for _, n := range Names() {
		b, err := files.ReadFile("js/" + n)
		if err != nil {
			return fmt.Errorf("writing the recorders: %w", err)
		}

		if err := os.WriteFile(filepath.Join(dir, n), b, 0o644); err != nil { //nolint:gosec // scripts a test runner reads
			return fmt.Errorf("writing the recorders: %w", err)
		}
	}

	return nil
}
