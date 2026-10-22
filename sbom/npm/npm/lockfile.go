// Package npm reads an npm workspace's bill of materials: the installed
// packages, the dependencies between them and the declared toolchains.
package npm

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// ErrCannotTell marks a lockfile whose packages can't be read (version 1
// nests a tree instead of listing install paths).
var ErrCannotTell = errors.New("cannot tell")

// ErrNoLockfile is the answer for a tree without package-lock.json or
// npm-shrinkwrap.json.
var ErrNoLockfile = fmt.Errorf("%w: no package-lock.json or npm-shrinkwrap.json, so nothing says which packages an install resolves", ErrCannotTell)

// ErrInvalid marks a lockfile that isn't well formed.
var ErrInvalid = errors.New("invalid lockfile")

// An Entry is one install path of a lockfile.
type Entry struct {
	// Path is the install path ("node_modules/a/node_modules/b"), or a
	// workspace's directory.
	Path    string `json:"-"`
	Name    string `json:"name"`
	Version string `json:"version"`
	// Resolved is a registry URL, a git URL with a commit, or a workspace
	// directory for a link.
	Resolved             string            `json:"resolved"`
	Integrity            string            `json:"integrity"`
	Link                 bool              `json:"link"`
	Dev                  bool              `json:"dev"`
	Optional             bool              `json:"optional"`
	DevOptional          bool              `json:"devOptional"` //nolint:tagliatelle // npm's field name
	Peer                 bool              `json:"peer"`
	Dependencies         map[string]string `json:"dependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"` //nolint:tagliatelle // npm's field name
	PeerDependencies     map[string]string `json:"peerDependencies"`     //nolint:tagliatelle // npm's field name
	DevDependencies      map[string]string `json:"devDependencies"`      //nolint:tagliatelle // npm's field name
	Engines              map[string]string `json:"engines"`
}

// A Lockfile is a package-lock.json or npm-shrinkwrap.json.
type Lockfile struct {
	File    string
	Dir     string
	Version int
	// Entries by install path; "" is the root package, and workspace
	// directories ("apps/web") hold the workspaces' own manifests.
	Entries map[string]*Entry
	// links, when set, say where each install path loads each dependency from,
	// for a layout Node's folder walk doesn't describe (pnpm's symlinked store).
	links map[string]map[string]string
}

type rawLockfile struct {
	LockfileVersion int               `json:"lockfileVersion"` //nolint:tagliatelle // npm's field name
	Packages        map[string]*Entry `json:"packages"`
}

// ReadLockfile reads a lockfile from the tree: npm's, or pnpm's by its name.
func ReadLockfile(tree, file string) (*Lockfile, error) {
	if path.Base(file) == filePnpmLock {
		return readPnpmLock(tree, file)
	}

	data, err := os.ReadFile(filepath.Join(tree, filepath.FromSlash(file))) //nolint:gosec // a lockfile the config names
	if err != nil {
		return nil, fmt.Errorf("lockfile: %w", err)
	}

	var raw rawLockfile
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrInvalid, file, err)
	}

	if raw.Packages == nil {
		// lockfileVersion 1 has only the nested "dependencies" tree.
		return nil, fmt.Errorf("%w: %s has no packages (lockfile version %d); npm 7 or later writes them", ErrCannotTell, file, raw.LockfileVersion)
	}

	dir := path.Dir(file)

	for p, e := range raw.Packages {
		if e == nil {
			return nil, fmt.Errorf("%w: %s: %q is null", ErrInvalid, file, p)
		}

		// Paths from the tree's root, so two lockfiles' copies of a package
		// stay apart in its digest.
		e.Path = path.Join(dir, p)
	}

	return &Lockfile{File: file, Dir: dir, Version: raw.LockfileVersion, Entries: raw.Packages}, nil
}

// InstallName is the name code imports a package by: the folder after the
// last node_modules/ of its install path. It differs from the package's own
// name for an alias ("my-react": "npm:react@18").
func InstallName(installPath string) (string, bool) {
	_, name, ok := strings.CutLast(installPath, "node_modules/")

	return name, ok
}

// Installed lists the install paths of packages, links and workspace
// directories left out, sorted.
func (l *Lockfile) Installed() []string {
	var out []string

	for p, e := range l.Entries {
		if _, ok := InstallName(p); ok && !e.Link {
			out = append(out, p)
		}
	}

	slices.Sort(out)

	return out
}

// Workspaces lists the root package and every workspace directory.
func (l *Lockfile) Workspaces() []string {
	var out []string

	for p := range l.Entries {
		if _, ok := InstallName(p); !ok {
			out = append(out, p)
		}
	}

	slices.Sort(out)

	return out
}

// Resolve finds the install path a package at from loads name from, the way
// Node does: its own node_modules, then each parent's, up to the root. A
// name that resolves to a workspace link answers the link's path.
func (l *Lockfile) Resolve(from, name string) (string, bool) {
	if l.links != nil {
		to, ok := l.links[from][name]

		return to, ok
	}

	dir := from

	for {
		cand := "node_modules/" + name
		if dir != "" {
			cand = dir + "/node_modules/" + name
		}

		if _, ok := l.Entries[cand]; ok {
			return cand, true
		}

		if dir == "" {
			return "", false
		}

		i := strings.LastIndex(dir, "/node_modules/")
		if i < 0 {
			if strings.HasPrefix(dir, "node_modules/") {
				dir = ""

				continue
			}

			// A workspace directory: its parent directories hold no
			// node_modules of the lockfile but the root's.
			dir = ""

			continue
		}

		dir = dir[:i]
	}
}
