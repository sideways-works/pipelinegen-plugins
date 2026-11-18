package decls

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Skip tells whether a directory never holds a repository's own code: the
// go command ignores testdata, vendor and names starting with . or _, and
// bazel-* and node_modules hold others' files.
func Skip(name string) bool {
	return name == "testdata" || name == "vendor" || name == "node_modules" ||
		strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || strings.HasPrefix(name, "bazel-")
}

// Root is the absolute path of a tree with every symlink resolved.
// filepath.WalkDir doesn't descend a root that is a symlink, and paths
// made relative to one spelling of a root don't match those the go
// command reports under the other, so every walk starts from this.
func Root(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("tree %s: %w", root, err)
	}

	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("tree %s: %w", root, err)
	}

	return resolved, nil
}

// Modules lists the directories under root holding a go.mod, relative to
// root, "." for root itself, sorted. Symlinks inside the tree aren't
// followed, as the go command doesn't follow them for ./... either.
func Modules(root string) ([]string, error) {
	root, err := Root(root)
	if err != nil {
		return nil, fmt.Errorf("modules: %w", err)
	}

	var out []string

	err = filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if e.IsDir() {
			if p != root && Skip(e.Name()) {
				return filepath.SkipDir
			}

			return nil
		}

		if e.Name() == "go.mod" {
			out = append(out, rel(root, filepath.Dir(p)))
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("modules: %w", err)
	}

	slices.Sort(out)

	return out, nil
}

// Dirs lists the package directories of the module in dir, relative to root
// and sorted, without nested modules and those Skip names.
func Dirs(root, dir string) ([]string, error) {
	root, err := Root(root)
	if err != nil {
		return nil, fmt.Errorf("packages of %s: %w", dir, err)
	}

	top := filepath.Join(root, dir)

	var out []string

	err = filepath.WalkDir(top, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !e.IsDir() {
			return nil
		}

		if p != top {
			if Skip(e.Name()) {
				return filepath.SkipDir
			}

			if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
				return filepath.SkipDir
			}
		}

		if HasGo(p) {
			out = append(out, rel(root, p))
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("packages of %s: %w", dir, err)
	}

	return out, nil
}

func HasGo(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}

	return slices.ContainsFunc(entries, func(e fs.DirEntry) bool {
		return !e.IsDir() && strings.HasSuffix(e.Name(), ".go")
	})
}

func rel(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return filepath.ToSlash(p)
	}

	return filepath.ToSlash(r)
}
