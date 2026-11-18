package graph

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/sideways-works/pipelinegen-plugins/sbom/go/decls"
	"github.com/sideways-works/pipelinegen-plugins/sbom/go/goid"
)

// machineErrors are what the go command says when the module cache, not the
// tree, lacks something: the answer then depends on the machine, and the
// whole tree can't be told rather than one package.
var machineErrors = []string{
	"cannot find module providing package", "no required module provides package", "missing go.sum entry",
	"import lookup disabled", "module lookup disabled", "updates to go.mod needed",
}

// moduleError is an error that belongs to one module as a whole: an
// import outside the tree that fails for a reason of the tree's (a replace
// to a directory that isn't there, inconsistent vendoring). The module's
// packages are bound wide on their own.
type moduleError struct{ why string }

func (m *moduleError) Error() string { return m.why }

// errorSort sorts a module's load errors: those the module cache causes,
// those of imports outside the tree, and those of the tree's packages, by
// directory.
type errorSort struct {
	t                *Tree
	moduleDir        string
	byDir            map[string][]string
	plain            map[string]bool
	outside, machine []string
}

func (c *errorSort) add(p *packages.Package, raw string) {
	t := c.t
	msg := t.firstLine(raw, c.moduleDir)

	if t.fromMachine(raw) {
		c.machine = append(c.machine, msg)

		return
	}

	if fromLocalMachine(raw) && t.Uncacheable == "" {
		t.Uncacheable = "cgo needs a C compiler this machine lacks: " + msg
	}

	dir, ok := t.mods.dirOf(strings.TrimSuffix(strings.TrimSuffix(p.PkgPath, ".test"), "_test"))
	if !ok || t.outsideImport(raw) {
		c.outside = append(c.outside, msg)

		return
	}

	c.byDir[dir] = append(c.byDir[dir], msg)

	if !strings.Contains(p.ID, " [") && !strings.HasSuffix(p.PkgPath, "_test") && !strings.HasSuffix(p.PkgPath, ".test") {
		c.plain[dir] = true
	}
}

// brokenModule binds every package directory of a module wide on its own,
// with why; with asked packages, only those.
func (t *Tree) brokenModule(md, why string, o Options) error {
	dirs, err := decls.Dirs(t.Root, md)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrLoad, err)
	}

	broken := map[string]brokenPkg{}

	for _, d := range dirs {
		if o.Packages == nil || slices.Contains(o.Packages, d) {
			broken[d] = brokenPkg{first: why}
		}
	}

	t.dropUnclaimed(broken, o.Claimed)

	return t.addBroken(md, broken, o.Build)
}

type brokenPkg struct {
	first     string
	testsOnly bool
}

// brokenPackages are the tree's packages the go command or the type checker
// reported an error for, by directory, with the first error. Each is bound
// wide on its own, so one stale package leaves every other answer exact; an
// error outside the tree, or from the module cache, fails the load.
func (t *Tree) brokenPackages(pkgs []*packages.Package, moduleDir string, b decls.Build) (map[string]brokenPkg, error) {
	c := &errorSort{t: t, moduleDir: moduleDir, byDir: map[string][]string{}, plain: map[string]bool{}}

	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			c.add(p, e.Error())
		}
	})

	byDir, plain, outside, machine := c.byDir, c.plain, c.outside, c.machine

	if len(machine) > 0 {
		slices.Sort(machine)
		machine = slices.Compact(machine)

		if len(machine) > 5 {
			machine = append(machine[:5], fmt.Sprintf("and %d more", len(machine)-5))
		}

		return nil, fmt.Errorf("%w: %s", ErrLoad, strings.Join(machine, "; "))
	}

	if len(outside) > 0 {
		slices.Sort(outside)

		return nil, &moduleError{why: outside[0]}
	}

	out := make(map[string]brokenPkg, len(byDir))

	for dir, errs := range byDir {
		slices.Sort(errs)

		sp, err := decls.ReadWith(t.Root, dir, decls.Config{Build: b})
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrLoad, err)
		}

		if sp.Unparsed != "" {
			// go-sbom lists the files, not what they declare: the
			// package's own code can't be named either
			t.unparsed[dir] = true
			plain[dir] = true
		}

		out[dir] = brokenPkg{first: errs[0], testsOnly: !plain[dir]}
	}

	return out, nil
}

// gatedDirs adds to broken the directories of the module in moduleDir
// that hold Go files but no package the go command loaded: every file is
// behind build constraints the build doesn't meet. The go command may say
// nothing about them, yet a suite that sets other tags runs their tests.
// With asked set, only those directories count.
func (t *Tree) gatedDirs(moduleDir string, broken map[string]brokenPkg, asked []string) error {
	dirs, err := decls.Dirs(t.Root, moduleDir)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrLoad, err)
	}

	for _, dir := range dirs {
		if _, ok := broken[dir]; ok || t.pkgs[dir] != nil || (asked != nil && !slices.Contains(asked, dir)) {
			continue
		}

		if md, ok := t.mods.moduleOf(dir); !ok || md != moduleDir {
			continue
		}

		broken[dir] = brokenPkg{first: "build constraints exclude all Go files in " + dir}
	}

	return nil
}

// dropUnclaimed leaves out the broken packages none of whose Go files is
// claimed: the repository's config excludes them, so what they declare is
// no unit, and nothing they hold binds wide.
func (t *Tree) dropUnclaimed(broken map[string]brokenPkg, claimed map[string]bool) {
	if claimed == nil {
		return
	}

	for dir := range broken {
		entries, err := os.ReadDir(filepath.Join(t.Root, dir))
		if err != nil {
			continue // addBroken reports it
		}

		if slices.ContainsFunc(entries, func(e os.DirEntry) bool {
			return claimed[filepath.ToSlash(filepath.Join(dir, e.Name()))]
		}) {
			continue
		}

		delete(broken, dir)
		delete(t.pkgs, dir)
	}
}

// firstLine is an error's first line, with paths from the tree's root, so
// an answer is the same in every checkout of the tree. The go command heads
// a package's compiler output with "# <import path>" and names files from
// the module's directory.
func (t *Tree) firstLine(msg, moduleDir string) string {
	msg = strings.ReplaceAll(msg, t.Root+string(filepath.Separator), "")
	msg = strings.ReplaceAll(msg, t.Root, ".")

	for line := range strings.SplitSeq(msg, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "-: "))
		line = strings.TrimPrefix(line, "err: exit status 1: stderr: ")

		if line == "" || strings.HasPrefix(line, "# ") {
			continue
		}

		file, rest, ok := strings.Cut(line, ":")

		switch {
		case ok && filepath.IsAbs(file):
			// a file of the standard library or the module cache: from
			// its source root on, the same on every machine
			if _, after, found := strings.Cut(file, "/src/"); found {
				line = after + ":" + rest
			}
		case ok && moduleDir != "." && strings.HasSuffix(file, ".go") && !strings.HasPrefix(file, moduleDir+"/"):
			line = moduleDir + "/" + line
		}

		return line
	}

	return msg
}

// localMachine are errors that come from the machine's own tools rather
// than the tree or the module cache: the package is bound wide on its own,
// but the answer isn't cached, since another machine may build it.
var localMachine = []string{"C compiler", "could not import C ", "cgo: "}

func fromLocalMachine(msg string) bool {
	return slices.ContainsFunc(localMachine, func(m string) bool { return strings.Contains(msg, m) })
}

func (t *Tree) fromMachine(msg string) bool {
	return slices.ContainsFunc(machineErrors, func(m string) bool { return strings.Contains(msg, m) })
}

// outsideImport tells an import outside the tree that couldn't be read: it
// says nothing about the importing package's own code.
func (t *Tree) outsideImport(msg string) bool {
	_, rest, ok := strings.Cut(msg, "could not import ")
	if !ok {
		return false
	}

	importPath, _, _ := strings.Cut(rest, " ")
	_, inTree := t.mods.dirOf(importPath)

	return !inTree
}

// addBroken records the broken packages of the module in moduleDir, each
// with the units go-sbom lists for it that can't be told: all of them, or
// its tests' when only those fail.
func (t *Tree) addBroken(moduleDir string, broken map[string]brokenPkg, b decls.Build) error {
	for _, dir := range sortedKeys(broken) {
		bp := broken[dir]

		pi := t.pkgs[dir]
		if pi == nil {
			pi = &Package{Dir: dir, Module: moduleDir}
			t.pkgs[dir] = pi
		}

		entries, err := os.ReadDir(filepath.Join(t.Root, dir))
		if err != nil {
			return fmt.Errorf("%w: %w", ErrLoad, err)
		}

		for _, e := range entries {
			if f := filepath.ToSlash(filepath.Join(dir, e.Name())); !e.IsDir() && strings.HasSuffix(e.Name(), ".go") && !slices.Contains(pi.Files, f) {
				pi.Files = append(pi.Files, f)
			}
		}

		slices.Sort(pi.Files)

		pi.Broken = NotBuilding(dir, b, bp.first)
		pi.BrokenTests = bp.testsOnly

		if !bp.testsOnly && pi.Whole == "" {
			pi.Whole = pi.Broken
			pi.TestWhole = pi.Broken
		}

		sp, err := decls.ReadWith(t.Root, dir, decls.Config{Build: b})
		if err != nil {
			return fmt.Errorf("%w: %w", ErrLoad, err)
		}

		for _, u := range sp.Units {
			if u.ID.Kind() != goid.API && (!bp.testsOnly || testUnit(u.ID)) {
				pi.Units = append(pi.Units, u.ID)
			}
		}
	}

	return nil
}

func testUnit(id goid.ID) bool {
	return id.Test() || (id.Kind() == goid.Excluded && strings.HasSuffix(id.Name(), "_test.go"))
}

// NotBuilding is the reason given for a package that doesn't build: what
// is wrong, and what it costs until it is fixed.
func NotBuilding(dir string, b decls.Build, first string) string {
	return fmt.Sprintf("package %s does not build with tags [%s]: %s; tests that reach it run on every change until it builds or is excluded",
		dir, strings.Join(b.Context().BuildTags, " "), first)
}

func (t *Tree) Broken() map[string]string {
	out := map[string]string{}

	for dir, p := range t.pkgs {
		if p.Broken != "" {
			out[dir] = p.Broken
		}
	}

	return out
}

// reachesBroken tells whether a binding needs a package that doesn't build:
// it binds one of its units, links it, or holds the slice's own files. Such a
// slice depends on everything; a package whose tests alone fail counts only
// for its tests.
func (t *Tree) reachesBroken(b binding, s Slice) bool {
	broken := func(dir string, test bool) bool {
		p := t.pkgs[dir]

		return p != nil && p.Broken != "" && (test || !p.BrokenTests)
	}

	for dir := range b.linked {
		if broken(dir, false) {
			return true
		}
	}

	for i := range b.decls {
		if d := t.Decls[i]; broken(d.ID.Dir(), d.Test) {
			return true
		}
	}

	for _, f := range s.Sources {
		if broken(filepath.ToSlash(filepath.Dir(filepath.Clean(f))), true) {
			return true
		}
	}

	return slices.ContainsFunc(s.List, func(id string) bool { return broken(goid.ID(id).Dir(), goid.ID(id).Test()) })
}
