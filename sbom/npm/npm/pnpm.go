package npm

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// A pnpm lockfile (lockfileVersion 6.0 and 9.0) is read into npm's install
// paths: each importer's direct dependencies at <importer>/node_modules/<name>
// and every store package at node_modules/.pnpm/<key>/node_modules/<name>.
// Where each copy loads its dependencies from comes from the lockfile.

type pnpmDep struct {
	Specifier string `yaml:"specifier"`
	Version   string `yaml:"version"`
}

type pnpmImporter struct {
	Dependencies         map[string]pnpmDep `yaml:"dependencies"`
	DevDependencies      map[string]pnpmDep `yaml:"devDependencies"`      //nolint:tagliatelle // pnpm's field name
	OptionalDependencies map[string]pnpmDep `yaml:"optionalDependencies"` //nolint:tagliatelle // pnpm's field name
}

type pnpmPackage struct {
	Resolution struct {
		Integrity string `yaml:"integrity"`
		Tarball   string `yaml:"tarball"`
		Repo      string `yaml:"repo"`
		Commit    string `yaml:"commit"`
		Directory string `yaml:"directory"`
	} `yaml:"resolution"`
	Name                 string            `yaml:"name"`
	Version              string            `yaml:"version"`
	Engines              map[string]string `yaml:"engines"`
	Dependencies         map[string]string `yaml:"dependencies"`
	OptionalDependencies map[string]string `yaml:"optionalDependencies"` //nolint:tagliatelle // pnpm's field name
	Dev                  *bool             `yaml:"dev"`
}

type rawPnpmLock struct {
	// A lockfile of one project without workspaces lists its importer's
	// dependencies at the top level.
	pnpmImporter `yaml:",inline"`

	Importers map[string]pnpmImporter `yaml:"importers"`
	Packages  map[string]pnpmPackage  `yaml:"packages"`
	Snapshots map[string]pnpmPackage  `yaml:"snapshots"`
}

func isPnpmLock(tree, p string) bool {
	data, err := os.ReadFile(filepath.Join(tree, filepath.FromSlash(p))) //nolint:gosec // a candidate of the tree
	if err != nil {
		return false
	}

	return bytes.HasPrefix(bytes.TrimLeft(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), " \n"), []byte("lockfileVersion:"))
}

// readPnpmLock reads a pnpm lockfile. Versions before 6.0 key packages as
// "/name/1.0.0" and can't tell.
func readPnpmLock(tree, file string) (*Lockfile, error) {
	data, err := os.ReadFile(filepath.Join(tree, filepath.FromSlash(file))) //nolint:gosec // a lockfile the config names
	if err != nil {
		return nil, fmt.Errorf("lockfile: %w", err)
	}

	// the version first: older versions are shaped otherwise
	var head struct {
		LockfileVersion any `yaml:"lockfileVersion"` //nolint:tagliatelle // pnpm's field name
	}

	if err := yaml.Unmarshal(data, &head); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrInvalid, file, err)
	}

	version, err := strconv.ParseFloat(fmt.Sprint(head.LockfileVersion), 64)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: lockfileVersion %v", ErrInvalid, file, head.LockfileVersion)
	}

	if version < 6 {
		return nil, fmt.Errorf("%w: %s is a pnpm lockfile of version %v; pnpm 8 or later writes 6.0 or 9.0", ErrCannotTell, file, head.LockfileVersion)
	}

	var raw rawPnpmLock
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrInvalid, file, err)
	}

	if raw.Importers == nil {
		raw.Importers = map[string]pnpmImporter{".": raw.pnpmImporter}
	}

	r := &pnpmReader{
		dir: path.Dir(file), file: file, raw: raw, v6: version < 9,
		l: &Lockfile{File: file, Dir: path.Dir(file), Version: int(version), Entries: map[string]*Entry{}, links: map[string]map[string]string{}},
	}

	if err := r.read(); err != nil {
		return nil, err
	}

	return r.l, nil
}

type pnpmReader struct {
	dir, file string
	raw       rawPnpmLock
	v6        bool
	l         *Lockfile
	prod      map[string]bool
}

// key is a store key without the leading slash version 6 writes.
func (r *pnpmReader) key(k string) string {
	return strings.TrimPrefix(k, "/")
}

// snapshot is a store key's entry: its dependencies (snapshots in 9.0,
// packages in 6.0) and, from packages, its resolution.
func (r *pnpmReader) snapshot(k string) (pnpmPackage, bool) {
	if r.v6 {
		p, ok := r.raw.Packages["/"+k]
		if !ok {
			p, ok = r.raw.Packages[k]
		}

		return p, ok
	}

	s, ok := r.raw.Snapshots[k]
	if !ok {
		return pnpmPackage{}, false
	}

	pkg := r.raw.Packages[withoutPeers(k)]
	s.Resolution, s.Engines, s.Name, s.Version = pkg.Resolution, pkg.Engines, pkg.Name, pkg.Version

	return s, true
}

// withoutPeers drops the peer suffix of a key or version: "a@1.0.0(b@2)"
// is "a@1.0.0".
func withoutPeers(s string) string {
	before, _, _ := strings.Cut(s, "(")

	return before
}

func nameVersion(k string) (string, string) {
	base := withoutPeers(k)

	i := strings.LastIndex(base, "@")
	if i <= 0 {
		return base, ""
	}

	return base[:i], base[i+1:]
}

// target is the store key a dependency named name at version v loads: v is
// a version ("1.0.0(peer@1)"), or for an alias the real package's key
// ("react@17.0.2"). A link to a workspace has no key.
func target(name, v string) (string, bool) {
	if strings.HasPrefix(v, "link:") {
		return "", false
	}

	// version 6 writes an alias's key with its leading slash
	v = strings.TrimPrefix(v, "/")

	if at := strings.LastIndex(withoutPeers(v), "@"); at > 0 {
		return v, true
	}

	return name + "@" + v, true
}

func storePath(k string) string {
	name, _ := nameVersion(k)

	return "node_modules/.pnpm/" + strings.ReplaceAll(k, "/", "+") + "/node_modules/" + name
}

func (r *pnpmReader) read() error {
	r.prod = map[string]bool{}

	importers := make([]string, 0, len(r.raw.Importers))
	for k := range r.raw.Importers {
		importers = append(importers, k)
	}

	slices.Sort(importers)

	// what production dependencies reach, for the dev attribute
	var stack []string

	for _, imp := range importers {
		in := r.raw.Importers[imp]

		for _, deps := range []map[string]pnpmDep{in.Dependencies, in.OptionalDependencies} {
			for name, d := range deps {
				if k, ok := target(name, d.Version); ok && !r.prod[k] {
					r.prod[k] = true
					stack = append(stack, k)
				}
			}
		}
	}

	for len(stack) > 0 {
		k := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		s, _ := r.snapshot(k)
		for _, deps := range []map[string]string{s.Dependencies, s.OptionalDependencies} {
			for name, v := range deps {
				if t, ok := target(name, v); ok && !r.prod[t] {
					r.prod[t] = true
					stack = append(stack, t)
				}
			}
		}
	}

	for _, imp := range importers {
		if err := r.importer(imp, r.raw.Importers[imp]); err != nil {
			return err
		}
	}

	return r.store()
}

func (r *pnpmReader) importer(imp string, in pnpmImporter) error {
	w := path.Clean(imp)
	if w == "." {
		w = ""
	}

	ws := &Entry{Path: path.Join(r.dir, w), Dependencies: map[string]string{}, DevDependencies: map[string]string{}, OptionalDependencies: map[string]string{}}
	r.l.Entries[w] = ws
	r.l.links[w] = map[string]string{}

	for _, group := range []struct {
		deps map[string]pnpmDep
		into map[string]string
	}{{in.Dependencies, ws.Dependencies}, {in.DevDependencies, ws.DevDependencies}, {in.OptionalDependencies, ws.OptionalDependencies}} {
		for name, d := range group.deps {
			group.into[name] = d.Specifier

			k, ok := target(name, d.Version)
			if !ok {
				continue // a workspace link: no package of its own
			}

			p := path.Join(w, "node_modules", name)

			e, err := r.entry(k, p)
			if err != nil {
				return err
			}

			r.l.Entries[p] = e
			r.l.links[w][name] = p
			r.l.links[p] = r.deps(k)
		}
	}

	return nil
}

func (r *pnpmReader) store() error {
	keys := make([]string, 0)

	if r.v6 {
		for k := range r.raw.Packages {
			keys = append(keys, r.key(k))
		}
	} else {
		for k := range r.raw.Snapshots {
			keys = append(keys, k)
		}
	}

	slices.Sort(keys)

	for _, k := range keys {
		p := storePath(k)

		e, err := r.entry(k, p)
		if err != nil {
			return err
		}

		r.l.Entries[p] = e
		r.l.links[p] = r.deps(k)
	}

	return nil
}

func (r *pnpmReader) deps(k string) map[string]string {
	s, _ := r.snapshot(k)
	out := map[string]string{}

	for _, deps := range []map[string]string{s.Dependencies, s.OptionalDependencies} {
		for name, v := range deps {
			if t, ok := target(name, v); ok {
				if _, known := r.snapshot(t); known {
					out[name] = storePath(t)
				}
			}
		}
	}

	return out
}

func (r *pnpmReader) entry(k, p string) (*Entry, error) {
	s, ok := r.snapshot(k)
	if !ok {
		return nil, fmt.Errorf("%w: %s: %s is in no package entry", ErrInvalid, r.file, k)
	}

	name, version := nameVersion(k)
	if s.Name != "" {
		name = s.Name
	}

	if s.Version != "" {
		version = s.Version
	}

	resolved := s.Resolution.Tarball
	if s.Resolution.Repo != "" {
		resolved = s.Resolution.Repo + "#" + s.Resolution.Commit
	}

	if s.Resolution.Directory != "" {
		resolved = "file:" + s.Resolution.Directory
	}

	dev := !r.prod[k]
	if s.Dev != nil {
		dev = *s.Dev
	}

	return &Entry{
		Path: path.Join(r.dir, p), Name: name, Version: version, Resolved: resolved, Integrity: s.Resolution.Integrity,
		Dev: dev, Dependencies: s.Dependencies, OptionalDependencies: s.OptionalDependencies, Engines: s.Engines,
	}, nil
}
