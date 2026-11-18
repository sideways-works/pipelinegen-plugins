// Package observe turns what the recorders left in a slice's directories
// into the catalog units the slice looked up.
//
// A recorder writes locale-keys.*.jsonl files: a header naming the recorder,
// then one record per line (and an end line from the JavaScript recorders):
//
//	{"c": catalog, "load": true}                     the recorder runs for the catalog
//	{"c": catalog, "l": lang, "lang": true}          a check that the language is there
//	{"c": catalog, "l": lang, "n": ns, "has": true}  a check that a namespace is there
//	{"c": catalog, "l": lang, "n": ns, "k": key}     a lookup of a key, hit or miss
//	{"f": file, "k": key}                            a lookup in the namespace a catalog file holds
//	{"f": file, "has": true}                         a check that the file's namespace is there
//	{"cant": reason}                                 the recorder couldn't tell
package observe

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen-plugins/sbom/locale/catalog"
)

var ErrCantTell = errors.New("can't tell")

// ErrUnreadable is a recording that can't be read: the slice errors.
var ErrUnreadable = errors.New("unreadable recording")

const (
	Prefix = "locale-keys."
	Suffix = ".jsonl"
)

const (
	RuntimeGo      = "go"
	RuntimeVitest  = "vitest"
	RuntimeBrowser = "browser"
	RuntimeCollect = "collect"
)

type Header struct {
	Recorder string `json:"recorder"`
	Version  int    `json:"version"`
	Runtime  string `json:"runtime"`
	PID      int    `json:"pid,omitempty"`
}

type Record struct {
	Catalog string  `json:"c,omitempty"`
	File    string  `json:"f,omitempty"`
	Lang    string  `json:"l,omitempty"`
	NS      *string `json:"n,omitempty"`
	Key     *string `json:"k,omitempty"`
	Has     bool    `json:"has,omitempty"`
	IsLang  bool    `json:"lang,omitempty"`
	Load    bool    `json:"load,omitempty"`
	Cant    string  `json:"cant,omitempty"`
	End     bool    `json:"end,omitempty"`
}

// Answer is what a slice looked up: Touched holds the setup's too, and
// SetupLike what only the setup looked up.
type Answer struct {
	Touched   []string
	SetupLike []string
}

// Catalogs knows the claimed catalog files: which catalogs exist and
// whether each keeps one directory per language.
type Catalogs struct {
	known    bool
	nsLayout map[string]bool
	files    map[string]catalog.File
}

// NewCatalogs reads the claimed paths. With none at all (a core that
// passes no claims) every catalog a recording names is taken as it is.
func NewCatalogs(paths []string) Catalogs {
	c := Catalogs{known: len(paths) > 0, nsLayout: map[string]bool{}, files: map[string]catalog.File{}}

	for _, p := range paths {
		f, ok := catalog.Locate(p)
		if !ok {
			continue
		}

		c.files[p] = f
		c.nsLayout[f.Catalog] = c.nsLayout[f.Catalog] || f.NS != catalog.NoNS
	}

	return c
}

// Observe reads the setup's directory (dirs[0], which may be missing) and
// the slice's parts.
func Observe(dirs []string, cats Catalogs) (Answer, error) {
	if len(dirs) < 2 {
		return Answer{}, fmt.Errorf("%w: no part of the slice was named", ErrCantTell)
	}

	setup, err := readDir(dirs[0], cats, true)
	if err != nil {
		return Answer{}, err
	}

	own := map[string]bool{}

	for _, d := range dirs[1:] {
		ids, err := readDir(d, cats, false)
		if err != nil {
			return Answer{}, err
		}

		for id := range ids {
			own[id] = true
		}
	}

	var ans Answer

	for id := range setup {
		// the catalog's own unit says the recorder ran: a setup that shows
		// it covers the slice as much as the slice's own part
		if !own[id] && !isCatalog(id) {
			ans.SetupLike = append(ans.SetupLike, id)
		}

		own[id] = true
	}

	for id := range own {
		ans.Touched = append(ans.Touched, id)
	}

	slices.Sort(ans.Touched)
	slices.Sort(ans.SetupLike)

	if ans.Touched == nil {
		ans.Touched = []string{}
	}

	if ans.SetupLike == nil {
		ans.SetupLike = []string{}
	}

	return ans, nil
}

func isCatalog(id string) bool { return !strings.Contains(id, "#") }

// readDir reads one directory's files, and those of its numbered parts.
// A slice's directory without a recorder's file is no evidence: can't
// tell. The setup's may be missing or empty.
func readDir(dir string, cats Catalogs, setup bool) (map[string]bool, error) {
	files, err := filesIn(dir)
	if err != nil {
		return nil, err
	}

	if len(files) == 0 && !setup {
		return nil, fmt.Errorf("%w: %s holds no recording of locale-correlator's: the slice ran without a recorder or without collect", ErrCantTell, dir)
	}

	ids := map[string]bool{}

	for _, f := range files {
		if err := readFile(f, cats, ids); err != nil {
			return nil, err
		}
	}

	return ids, nil
}

func filesIn(dir string) ([]string, error) {
	var out []string

	// a directory handed over as a symlink is read where it points
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}

	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == dir && errors.Is(err, fs.ErrNotExist) {
				return filepath.SkipAll
			}

			return err
		}

		if d.IsDir() {
			if p != dir && strings.Count(strings.TrimPrefix(p, dir), string(filepath.Separator)) > 1 {
				return filepath.SkipDir
			}

			return nil
		}

		if n := d.Name(); strings.HasPrefix(n, Prefix) && strings.HasSuffix(n, Suffix) && d.Type().IsRegular() {
			out = append(out, p)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnreadable, err)
	}

	slices.Sort(out)

	return out, nil
}

func readFile(name string, cats Catalogs, ids map[string]bool) error {
	b, err := os.ReadFile(name) //nolint:gosec // a recording the batch left
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnreadable, err)
	}

	if len(b) == 0 || b[len(b)-1] != '\n' {
		return fmt.Errorf("%w: %s was cut short (no whole last line)", ErrCantTell, name)
	}

	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var (
		h     Header
		first = true
		ended bool
	)

	for sc.Scan() {
		line := sc.Bytes()

		if first {
			first = false

			if err := json.Unmarshal(line, &h); err != nil || h.Recorder != "locale-keys" {
				return fmt.Errorf("%w: %s has no recorder's header", ErrCantTell, name)
			}

			if h.Version != 1 {
				return fmt.Errorf("%w: %s is of version %d, which this plugin doesn't read", ErrCantTell, name, h.Version)
			}

			continue
		}

		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			return fmt.Errorf("%w: %s: %w", ErrUnreadable, name, err)
		}

		if ended {
			return fmt.Errorf("%w: %s has lines after its end", ErrUnreadable, name)
		}

		if r.End {
			ended = true

			continue
		}

		if r.Cant != "" {
			return fmt.Errorf("%w: %s", ErrCantTell, r.Cant)
		}

		if err := cats.add(r, ids); err != nil {
			return fmt.Errorf("%w (%s)", err, name)
		}
	}

	if err := sc.Err(); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrUnreadable, name, err)
	}

	if (h.Runtime == RuntimeVitest || h.Runtime == RuntimeBrowser) && !ended {
		return fmt.Errorf("%w: %s was cut short (no end line)", ErrCantTell, name)
	}

	return nil
}

// name is the catalog, language and namespace a record looked in; an
// empty catalog for a JSON file no claim names, whose paths to a slice
// stay as they are, so a recording of it narrows nothing.
func (c Catalogs) name(r Record) (cat, lang, ns string, err error) {
	switch {
	case r.File != "":
		f, claimed := c.files[r.File]
		if !claimed && !c.known {
			f, claimed = catalog.Locate(r.File)
		}

		if !claimed {
			return "", "", "", nil
		}

		return f.Catalog, f.Lang, f.NS, nil
	case r.Catalog == "":
		return "", "", "", fmt.Errorf("%w: a record names no catalog", ErrUnreadable)
	case !catalog.InTree(r.Catalog):
		return "", "", "", fmt.Errorf("%w: a record names the catalog %q, which is outside the tree", ErrCantTell, r.Catalog)
	}

	if _, ok := c.nsLayout[r.Catalog]; !ok && c.known {
		return "", "", "", fmt.Errorf("%w: a recorder names the catalog %s, which holds no catalog file locale-correlator claims", ErrCantTell, r.Catalog)
	}

	ns = catalog.NoNS
	if r.NS != nil && *r.NS != "" && (c.nsLayout[r.Catalog] || !c.known) {
		ns = *r.NS
	}

	return r.Catalog, r.Lang, ns, nil
}

func (c Catalogs) add(r Record, ids map[string]bool) error {
	cat, lang, ns, err := c.name(r)
	if err != nil || cat == "" {
		return err
	}

	ids[catalog.ID(cat)] = true

	switch {
	case r.Load:
	case r.IsLang:
		ids[catalog.LangID(cat, lang)] = true
	case r.Has:
		ids[catalog.NSID(cat, lang, ns)] = true
	case r.Key != nil:
		ids[catalog.KeyID(cat, lang, ns, *r.Key)] = true
	default:
		return fmt.Errorf("%w: a record of %s says nothing it looked up", ErrUnreadable, cat)
	}

	return nil
}
