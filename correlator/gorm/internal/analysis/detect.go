package analysis

import (
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/sideways-works/pipelinegen-sbom-go/goid"
	"golang.org/x/mod/modfile"
)

// The files the Go tool reads to load a module, and the directories it
// skips.
const (
	goMod       = "go.mod"
	goSum       = "go.sum"
	goWork      = "go.work"
	dirTestdata = "testdata"
	dirVendor   = "vendor"
)

// The roles of the files the correlator claims.
const (
	// RoleModule is a go.mod that requires GORM: its module is loaded.
	RoleModule = "module"
	// RoleUser is a Go file that imports GORM.
	RoleUser = "gorm-user"
	// RoleCode is any other Go file: one that doesn't import GORM can still
	// change what a statement reaches (a TableName method), so a new one makes
	// pipelinegen ask again.
	RoleCode = "code"
	// RoleModuleFile is any other go.mod, a go.sum or a go.work: what the
	// Go tool reads to load a module.
	RoleModuleFile = "module-file"
)

// Candidates are the paths worth showing the correlator.
var Candidates = []string{"**/go.mod", "**/go.sum", "**/go.work", "**/*.go"}

// ErrDetect wraps a failure to read the tree while detecting modules.
var ErrDetect = errors.New("detecting GORM modules")

// DetectFile says whether a file is the correlator's, from its name and
// content alone, and in which role. Files in directories the Go tool skips
// are not claimed.
func DetectFile(name string, content []byte) (string, bool) {
	if unbuilt(name) {
		return "", false
	}

	switch base := path.Base(name); {
	case base == goMod:
		f, err := modfile.ParseLax(name, content, nil)
		if err != nil {
			return "", false
		}

		for _, r := range f.Require {
			if r.Mod.Path == gormPath && !r.Indirect {
				return RoleModule, true
			}
		}

		return RoleModuleFile, true
	case base == goSum || base == goWork:
		return RoleModuleFile, true
	case strings.HasSuffix(name, ".go"):
		// a file that doesn't parse is still code the module holds
		f, err := parser.ParseFile(token.NewFileSet(), name, content, parser.ImportsOnly)
		if err != nil {
			return RoleCode, true
		}

		for _, imp := range f.Imports {
			if p, err := strconv.Unquote(imp.Path.Value); err == nil && (p == gormPath || strings.HasPrefix(p, gormPath+"/")) {
				return RoleUser, true
			}
		}

		return RoleCode, true
	}

	return "", false
}

func unbuilt(name string) bool {
	for d := range strings.SplitSeq(path.Dir(name), "/") {
		if d == dirTestdata || d == dirVendor || d == "node_modules" || (d != "." && (strings.HasPrefix(d, ".") || strings.HasPrefix(d, "_"))) {
			return true
		}
	}

	return false
}

// ClaimedInputs lists, from the claimed files, what the analysis of a module
// reads, as ModuleInputs does from the tree. A file belongs to the deepest
// module above it.
func ClaimedInputs(claimed []string, mod string, replaces []string) []string {
	var roots []string

	for _, p := range claimed {
		if path.Base(p) == goMod {
			roots = append(roots, path.Dir(p))
		}
	}

	owner := func(p string) string {
		best := ""
		found := false

		for _, r := range roots {
			if within(r, p) && (!found || len(r) > len(best)) {
				best, found = r, true
			}
		}

		return best
	}

	dirs := append([]string{mod}, replaces...)

	var out []string

	for _, p := range claimed {
		base := path.Base(p)

		if p == goWork {
			out = append(out, p)

			continue
		}

		for i, dir := range dirs {
			if owner(p) != dir {
				continue
			}

			if strings.HasSuffix(p, ".go") || path.Dir(p) == dir && (base == goMod || i == 0 && base == goSum) {
				out = append(out, p)
			}
		}
	}

	sort.Strings(out)

	return slices.Compact(out)
}

// DetectModules lists the directories, relative to root, of the Go modules
// whose go.mod requires GORM, skipping what the Go tool skips and
// node_modules.
func DetectModules(root string) ([]string, error) {
	var mods []string

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			name := d.Name()
			if p != root && (name == dirTestdata || name == dirVendor || name == "node_modules" ||
				strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}

			return nil
		}

		if d.Name() != goMod {
			return nil
		}

		b, err := os.ReadFile(p) //nolint:gosec // a go.mod inside the tree being analysed
		if err != nil {
			return fmt.Errorf("reading %s: %w", p, err)
		}

		if role, ok := DetectFile(relPath(root, p), b); ok && role == RoleModule {
			mods = append(mods, relPath(root, filepath.Dir(p)))
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDetect, err)
	}

	sort.Strings(mods)

	return mods, nil
}

// LocalReplaces lists the directories, relative to root, that the modules'
// go.mod files replace dependencies with: the type checker reads them too.
func LocalReplaces(root string, mods []string) ([]string, error) {
	seen := map[string]bool{}

	var out []string

	for _, m := range mods {
		p := filepath.Join(root, m, goMod)

		b, err := os.ReadFile(p) //nolint:gosec // a go.mod inside the tree being analysed
		if err != nil {
			return nil, fmt.Errorf("%w: reading %s: %w", ErrDetect, p, err)
		}

		// ParseLax drops replace directives; the main module keeps them
		f, err := modfile.Parse(p, b, nil)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrDetect, p, err)
		}

		for _, r := range f.Replace {
			if r.New.Version != "" || filepath.IsAbs(r.New.Path) {
				continue
			}

			dir := relPath(root, filepath.Join(root, m, r.New.Path))
			if !seen[dir] {
				seen[dir] = true
				out = append(out, dir)
			}
		}
	}

	sort.Strings(out)

	return out, nil
}

// ModuleInputs lists the files, relative to root, that the analysis of a
// module reads: its Go and module files, those of the directories it
// replaces dependencies with, and a go.work at the root.
func ModuleInputs(root, mod string, replaces []string) ([]string, error) {
	var out []string

	for i, dir := range append([]string{mod}, replaces...) {
		files, err := goFiles(root, dir, i == 0)
		if err != nil {
			return nil, err
		}

		out = append(out, files...)
	}

	if _, err := os.Stat(filepath.Join(root, goWork)); err == nil {
		out = append(out, goWork)
	}

	sort.Strings(out)

	return out, nil
}

func goFiles(root, dir string, sum bool) ([]string, error) {
	top := filepath.Join(root, dir)

	var out []string

	err := filepath.WalkDir(top, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		name := d.Name()

		if d.IsDir() {
			if p == top {
				return nil
			}

			if name == dirTestdata || name == dirVendor || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				return filepath.SkipDir
			}

			if _, err := os.Stat(filepath.Join(p, goMod)); err == nil {
				return filepath.SkipDir
			}

			return nil
		}

		if strings.HasSuffix(name, ".go") || filepath.Dir(p) == top && (name == goMod || sum && name == goSum) {
			out = append(out, relPath(root, p))
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%w: listing %s: %w", ErrDetect, dir, err)
	}

	return out, nil
}

// Unreadable answers for a module the Go tool can't load because a dependency
// is missing from the module cache: every declaration of its GORM-importing
// files can't be told.
func Unreadable(root, mod, reason string, inputs []string) ([]CannotTell, error) {
	var out []CannotTell

	for _, p := range inputs {
		if !strings.HasSuffix(p, ".go") || !within(mod, p) {
			continue
		}

		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p))) //nolint:gosec // a file of the tree being analysed
		if err != nil {
			return nil, fmt.Errorf("%w: reading %s: %w", ErrDetect, p, err)
		}

		if role, ok := DetectFile(p, b); !ok || role != RoleUser {
			continue
		}

		f, err := parser.ParseFile(token.NewFileSet(), p, b, parser.SkipObjectResolution)
		if err != nil {
			continue
		}

		for _, d := range goid.File(path.Dir(p), path.Base(p), f) {
			if d.ID != "" {
				out = append(out, CannotTell{Unit: string(d.ID), Reasons: []string{reason}})
			}
		}
	}

	return out, nil
}

func within(mod, p string) bool {
	return mod == "." || strings.HasPrefix(p, mod+"/")
}
