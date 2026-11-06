// Package playwrightdriver embeds marks.mjs, the module a suite's coverage
// fixture imports to mark each spec file in a recorded stack bundle.
package playwrightdriver

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

// MarksName is the module's file name.
const MarksName = "marks.mjs"

//go:embed marks.mjs
var marks []byte

// Marks returns a copy of the embedded marks.mjs.
func Marks() []byte {
	return append([]byte(nil), marks...)
}

// WriteMarks writes marks.mjs into dir, creating dir, and returns its path.
func WriteMarks(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("writing %s: %w", MarksName, err)
	}

	path := filepath.Join(dir, MarksName)

	if err := os.WriteFile(path, marks, 0o644); err != nil { //nolint:gosec // a script the test runner reads
		return "", fmt.Errorf("writing %s: %w", MarksName, err)
	}

	return path, nil
}
