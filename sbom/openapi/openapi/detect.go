package openapi

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

const (
	// RoleContract is an OpenAPI document: its top level has openapi (or
	// swagger) and paths.
	RoleContract = "contract"
	// RoleFragment is a file a contract refers to for some of its parts.
	RoleFragment = "fragment"
)

var CandidatePatterns = []string{"**/*.yaml", "**/*.yml", "**/*.json"}

type Detection struct {
	Path string `json:"path"`
	Role string `json:"role"`
	// References are the files a contract's refs name, which the claim reads
	// along.
	References []string `json:"depends,omitempty"`
	// ByUnits names the narrowing under which a test that depends on the
	// file is bound to the file's units it touched instead.
	ByUnits string `json:"by_units,omitempty"`
}

// Detect claims the contracts among the candidates, each with the files it
// refers to. A YAML file that isn't an OpenAPI document is someone else's.
func Detect(tree string, paths []string) []Detection {
	var out []Detection

	for _, p := range paths {
		data, err := os.ReadFile(filepath.Join(tree, filepath.FromSlash(p))) //nolint:gosec // a candidate of the tree
		if err != nil || !looksLikeContract(data) {
			continue
		}

		v, err := decode(data)
		if err != nil {
			continue
		}

		doc := asMap(v)
		if !isDocument(doc) {
			continue
		}

		if _, ok := doc["paths"]; !ok {
			if _, ok := doc["webhooks"]; !ok {
				continue
			}
		}

		out = append(out, Detection{Path: p, Role: RoleContract, References: references(p, doc), ByUnits: NarrowDocument})
	}

	return out
}

func looksLikeContract(data []byte) bool {
	return bytes.Contains(data, []byte("openapi")) || bytes.Contains(data, []byte("swagger"))
}

func references(file string, doc any) []string {
	var out []string

	var walk func(v any)

	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if raw, ok := t[keyRef].(string); ok {
				name, _, _ := strings.Cut(raw, "#")
				if name != "" && !strings.Contains(name, "://") {
					out = append(out, path.Clean(path.Join(path.Dir(file), name)))
				}
			}

			for _, val := range t {
				walk(val)
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		}
	}

	walk(doc)
	slices.Sort(out)

	return slices.Compact(out)
}

// Candidates walks the tree for files CandidatePatterns name, for running the
// plugin by hand.
func Candidates(tree string) ([]string, error) {
	var out []string

	err := filepath.WalkDir(tree, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		name := e.Name()

		if e.IsDir() {
			if p != tree && (name == "node_modules" || name == "vendor" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}

			return nil
		}

		switch path.Ext(name) {
		case ".yaml", ".yml", ".json":
			rel, err := filepath.Rel(tree, p)
			if err != nil {
				return err //nolint:wrapcheck // returned to the walk below, which wraps it
			}

			out = append(out, filepath.ToSlash(rel))
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("candidates: %w", err)
	}

	return out, nil
}

// Documents turns detections into the documents to read: every contract
// and every file a contract refers to.
func Documents(found []Detection) []Document {
	var paths []string

	for _, d := range found {
		paths = append(paths, d.Path)
		paths = append(paths, d.References...)
	}

	slices.Sort(paths)

	docs := make([]Document, 0, len(paths))
	for _, p := range slices.Compact(paths) {
		docs = append(docs, Document{Path: p})
	}

	return docs
}
