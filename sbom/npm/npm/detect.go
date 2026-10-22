package npm

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Roles a detected file can have.
const (
	RoleLockfile    = "lockfile"
	RoleManifest    = "manifest"
	RoleNodeVersion = "node-version"
)

// The files npm's tools read, by name.
const (
	fileManifest    = "package.json"
	filePackageLock = "package-lock.json"
	fileShrinkwrap  = "npm-shrinkwrap.json"
	filePnpmLock    = "pnpm-lock.yaml"
	fileNvmrc       = ".nvmrc"
	fileNodeVersion = ".node-version"
)

// CandidatePatterns are the paths worth reading to detect npm's files.
var CandidatePatterns = []string{
	"**/package.json", "**/package-lock.json", "**/npm-shrinkwrap.json", "**/pnpm-lock.yaml", "**/.nvmrc", "**/.node-version",
}

// A Detection is one file this plugin claims.
type Detection struct {
	Path string `json:"path"`
	Role string `json:"role"`
}

// Detect claims npm's files among the candidates. Files under node_modules
// belong to installed packages and are left out.
func Detect(tree string, paths []string) []Detection {
	var out []Detection

	for _, p := range paths {
		if slices.Contains(strings.Split(p, "/"), "node_modules") {
			continue
		}

		switch path.Base(p) {
		case fileNvmrc, fileNodeVersion:
			out = append(out, Detection{Path: p, Role: RoleNodeVersion})
		case filePackageLock, fileShrinkwrap:
			if hasKey(tree, p, "lockfileVersion") {
				out = append(out, Detection{Path: p, Role: RoleLockfile})
			}
		case filePnpmLock:
			if isPnpmLock(tree, p) {
				out = append(out, Detection{Path: p, Role: RoleLockfile})
			}
		case fileManifest:
			if hasKey(tree, p, "") {
				out = append(out, Detection{Path: p, Role: RoleManifest})
			}
		}
	}

	return out
}

func hasKey(tree, p, key string) bool {
	data, err := os.ReadFile(filepath.Join(tree, filepath.FromSlash(p))) //nolint:gosec // a candidate of the tree
	if err != nil {
		return false
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return false
	}

	if key == "" {
		return true
	}

	_, ok := m[key]

	return ok
}

// Candidates walks the tree for the files CandidatePatterns name.
// pipelinegen sends candidates itself; this is for running by hand.
func Candidates(tree string) ([]string, error) {
	var out []string

	err := filepath.WalkDir(tree, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		name := e.Name()

		if e.IsDir() {
			if p != tree && (name == "node_modules" || name == ".git" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}

			return nil
		}

		switch name {
		case fileManifest, filePackageLock, fileShrinkwrap, filePnpmLock, fileNvmrc, fileNodeVersion:
			rel, err := filepath.Rel(tree, p)
			if err != nil {
				return err //nolint:wrapcheck // wrapped below
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

func rank(p string) int {
	switch path.Base(p) {
	case fileShrinkwrap:
		return 2
	case filePackageLock:
		return 1
	default:
		return 0
	}
}

// Lockfiles picks the lockfiles from detections, one per directory.
func Lockfiles(found []Detection) []string {
	byDir := map[string]string{}

	for _, d := range found {
		if d.Role != RoleLockfile {
			continue
		}

		// npm reads npm-shrinkwrap.json before package-lock.json, and either wins
		// over a pnpm-lock.yaml beside it.
		dir := path.Dir(d.Path)
		if cur, ok := byDir[dir]; !ok || rank(d.Path) > rank(cur) {
			byDir[dir] = d.Path
		}
	}

	out := make([]string, 0, len(byDir))
	for _, p := range byDir {
		out = append(out, p)
	}

	slices.Sort(out)

	return out
}
