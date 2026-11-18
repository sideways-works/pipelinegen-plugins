// Package note is what this correlator shares with its sbom (templates/sbom): which files are
// notes and how a note's unit is spelled. A real plugin pair keeps it in one library both import.
package note

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// Namespace is the sbom's namespace: every unit it lists is note:<path>.
const Namespace = "note"

// Header is a note's first line, the evidence that a file is one.
const Header = "# note"

// Candidates are the paths the core offers detect.
var Candidates = []string{"**/*.note"}

// ID is the unit of the note at path, relative to the tree.
func ID(path string) string { return Namespace + ":" + path }

// Read is the note at path in tree, and whether it is one: a file that starts with Header.
func Read(tree, path string) ([]byte, bool, error) {
	data, err := os.ReadFile(filepath.Join(tree, filepath.FromSlash(path))) //nolint:gosec // a path the core hands in
	if err != nil {
		return nil, false, err //nolint:wrapcheck // the path is in it
	}

	first, _, _ := bytes.Cut(data, []byte("\n"))

	return data, strings.TrimSpace(string(first)) == Header, nil
}

// Digest changes exactly when a note's text does; blank lines and trailing space are no change.
func Digest(data []byte) string {
	h := sha256.New()

	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if line := strings.TrimRight(sc.Text(), " \t"); line != "" {
			h.Write([]byte(line + "\n"))
		}
	}

	return hex.EncodeToString(h.Sum(nil))
}

// See are the notes a note names on its "see: <path>" lines, as paths relative to the tree.
func See(data []byte) []string {
	var out []string

	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if p, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "see:"); ok && strings.TrimSpace(p) != "" {
			out = append(out, filepath.ToSlash(filepath.Clean(strings.TrimSpace(p))))
		}
	}

	return out
}
