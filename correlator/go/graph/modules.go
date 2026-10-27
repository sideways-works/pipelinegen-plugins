package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/sideways-works/pipelinegen-sbom-go/decls"
)

type module struct {
	// Dir is relative to the tree's root, "." for the root itself.
	Dir  string
	Path string
}

type modules []module

// ErrModule wraps a go.mod that can't be read.
var ErrModule = errors.New("module")

func findModules(root string) (modules, error) {
	dirs, err := decls.Modules(root)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrModule, err)
	}

	if len(dirs) == 0 {
		// binding nothing would leave every slice out
		return nil, fmt.Errorf("%w: no go.mod under %s", ErrModule, root)
	}

	mods := make(modules, 0, len(dirs))

	for _, d := range dirs {
		b, err := os.ReadFile(filepath.Join(root, d, "go.mod")) //nolint:gosec // a go.mod of the tree
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrModule, err)
		}

		mp := modfile.ModulePath(b)
		if mp == "" {
			return nil, fmt.Errorf("%w: %s/go.mod has no module line", ErrModule, d)
		}

		mods = append(mods, module{Dir: d, Path: mp})
	}

	return mods, nil
}

func (ms modules) moduleOf(dir string) (string, bool) {
	best := ""
	found := false

	for _, m := range ms {
		if (m.Dir == "." || dir == m.Dir || strings.HasPrefix(dir, m.Dir+"/")) && (!found || len(m.Dir) > len(best) || best == ".") {
			best, found = m.Dir, true
		}
	}

	return best, found
}

func (ms modules) dirOf(importPath string) (string, bool) {
	best := -1

	for i, m := range ms {
		if (importPath == m.Path || strings.HasPrefix(importPath, m.Path+"/")) &&
			(best < 0 || len(m.Path) > len(ms[best].Path)) {
			best = i
		}
	}

	if best < 0 {
		return "", false
	}

	m := ms[best]

	return path.Clean(path.Join(m.Dir, strings.TrimPrefix(importPath, m.Path))), true
}

type listed struct {
	ImportPath string `json:"ImportPath"` //nolint:tagliatelle // the go command's names
	Dir        string `json:"Dir"`        //nolint:tagliatelle // as above
	Name       string `json:"Name"`       //nolint:tagliatelle // as above
	ForTest    string `json:"ForTest"`    //nolint:tagliatelle // as above
	Standard   bool   `json:"Standard"`   //nolint:tagliatelle // as above
	Module     *struct {
		Path string `json:"Path"` //nolint:tagliatelle // as above
	} `json:"Module"` //nolint:tagliatelle // as above
	Deps    []string `json:"Deps"`    //nolint:tagliatelle // as above
	Imports []string `json:"Imports"` //nolint:tagliatelle // as above
}

// moduleDeps notes, for each package of the module in dir, the modules of
// every package it links, transitively. Third-party code runs through
// callbacks coverage never shows, so this binds by what is linked, never by
// what ran.
func (t *Tree) moduleDeps(ctx context.Context, dir string, env []string, b decls.Build, pats []string) error {
	args := append([]string{"list", "-deps", "-test", "-e", "-json=ImportPath,Dir,Name,ForTest,Standard,Module,Deps,Imports"}, BuildFlags(b)...)
	cmd := exec.CommandContext(ctx, "go", append(args, pats...)...) //nolint:gosec // the go command, with the build's flags
	cmd.Dir = filepath.Join(t.Root, dir)
	cmd.Env = env

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("%w: go list in %s: %w: %s", ErrModule, dir, err, stderr.String())
	}

	var all []listed

	dec := json.NewDecoder(bytes.NewReader(out))

	for {
		var l listed
		if err := dec.Decode(&l); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return fmt.Errorf("%w: go list in %s: %w", ErrModule, dir, err)
		}

		all = append(all, l)
	}

	moduleOf := map[string]string{}

	for _, l := range all {
		if !l.Standard && l.Module != nil {
			moduleOf[l.ImportPath] = l.Module.Path
		}
	}

	byPath := map[string]listed{}

	for _, l := range all {
		byPath[l.ImportPath] = l
	}

	for _, l := range all {
		t.addDeps(l, moduleOf)
		t.addOwnDeps(l, byPath, moduleOf)
		t.addListed(l)
	}

	return nil
}

type listedPackage struct {
	Name    string
	Imports []string
}

// addListed notes a package of the tree, outside its tests. A package listed
// only as a test's variant ("p [q.test]") counts unless the package itself is
// listed too.
func (t *Tree) addListed(l listed) {
	importPath, variant, _ := strings.Cut(l.ImportPath, " ")
	if strings.HasSuffix(importPath, ".test") || strings.HasSuffix(l.Name, "_test") || (variant != "" && l.ForTest == importPath) {
		return
	}

	dir, ok := t.mods.dirOf(importPath)
	if !ok {
		return
	}

	// a replace or a workspace builds the package from another directory
	// than its module path names: the purity proof, which reads syntax by
	// the module path, proves nothing of it
	if l.Dir != "" && relPath(t.Root, l.Dir) != dir {
		t.misplaced[importPath] = true
	}

	if _, plain := t.listed[dir]; plain && variant != "" {
		return
	}

	lp := listedPackage{Name: l.Name}

	for _, im := range l.Imports {
		im, _, _ = strings.Cut(im, " ")
		if d, ok := t.mods.dirOf(im); ok && d != dir {
			lp.Imports = appendNew(lp.Imports, d)
		}
	}

	t.listed[dir] = lp
}

// addOwnDeps notes on a listed package's directory the modules it links
// without going through another package of the tree, so a package's answer
// never depends on what another package imports.
func (t *Tree) addOwnDeps(l listed, byPath map[string]listed, moduleOf map[string]string) {
	importPath, _, _ := strings.Cut(l.ImportPath, " ")
	if strings.HasSuffix(importPath, ".test") {
		return
	}

	pkgDir, ok := t.mods.dirOf(strings.TrimSuffix(importPath, "_test"))
	if !ok {
		return
	}

	p := t.pkgs[pkgDir]
	if p == nil {
		return
	}

	test := l.ForTest != "" || strings.HasSuffix(importPath, "_test")
	seen := map[string]bool{}

	var visit func(path string)

	visit = func(path string) {
		if seen[path] {
			return
		}

		seen[path] = true

		dep, ok := byPath[path]
		if !ok {
			base, _, _ := strings.Cut(path, " ")
			if dep, ok = byPath[base]; !ok {
				return
			}
		}

		base, _, _ := strings.Cut(dep.ImportPath, " ")

		m, ok := moduleOf[base]
		if !ok || t.firstParty(m) {
			return
		}

		if test {
			p.OwnTestDeps = appendNew(p.OwnTestDeps, m)
		} else {
			p.OwnDeps = appendNew(p.OwnDeps, m)
		}

		for _, im := range dep.Imports {
			visit(im)
		}
	}

	for _, im := range l.Imports {
		visit(im)
	}
}

func (t *Tree) addDeps(l listed, moduleOf map[string]string) {
	// a test variant is listed as "p [p.test]"; the test main as "p.test"
	importPath, _, _ := strings.Cut(l.ImportPath, " ")
	if strings.HasSuffix(importPath, ".test") {
		return
	}

	pkgDir, ok := t.mods.dirOf(strings.TrimSuffix(importPath, "_test"))
	if !ok {
		return
	}

	p := t.pkgs[pkgDir]
	if p == nil {
		return
	}

	test := l.ForTest != "" || strings.HasSuffix(importPath, "_test")

	for _, d := range l.Deps {
		dep, _, _ := strings.Cut(d, " ")

		m, ok := moduleOf[dep]
		if !ok || t.firstParty(m) {
			continue
		}

		if test {
			p.TestDeps = appendNew(p.TestDeps, m)
		} else {
			p.Deps = appendNew(p.Deps, m)
		}
	}
}

func (t *Tree) firstParty(modulePath string) bool {
	return slices.ContainsFunc(t.mods, func(m module) bool { return m.Path == modulePath })
}

func appendNew(s []string, v string) []string {
	if slices.Contains(s, v) {
		return s
	}

	return append(s, v)
}

func (t *Tree) readRequires(dir string) error {
	b, err := os.ReadFile(filepath.Join(t.Root, dir, "go.mod")) //nolint:gosec // a go.mod of the tree
	if err != nil {
		return fmt.Errorf("%w: %w", ErrModule, err)
	}

	f, err := modfile.ParseLax(dir+"/go.mod", b, nil)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrModule, err)
	}

	for _, r := range f.Require {
		if !t.firstParty(r.Mod.Path) {
			t.requires[dir] = appendNew(t.requires[dir], r.Mod.Path)
		}
	}

	// a lax parse drops replace lines, which only a main module has; a
	// go.mod a strict parse refuses keeps its links to every module
	strict, err := modfile.Parse(dir+"/go.mod", b, nil)
	if err != nil {
		return nil //nolint:nilerr // the lax parse above answered
	}

	for _, r := range strict.Replace {
		if r.New.Version == "" {
			if t.dirReplaced[dir] == nil {
				t.dirReplaced[dir] = map[string]bool{}
			}

			t.dirReplaced[dir][r.Old.Path] = true
		}
	}

	return nil
}

// workOf is the go.work the go command finds for a module of the tree: the
// first in its directory or the ones above it, up to the tree's root.
func workOf(root, dir string) string {
	for d := path.Clean(dir); ; d = path.Dir(d) {
		w := path.Join(d, "go.work")
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(w))); err == nil {
			return w
		}

		if d == "." || d == "/" {
			return ""
		}
	}
}
