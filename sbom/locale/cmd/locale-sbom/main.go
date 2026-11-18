package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/plugin"

	"github.com/sideways-works/pipelinegen-plugins/sbom/locale/catalog"
)

var Version = "dev"

const roleCatalog = "catalog"

// inlineUnits is the most units an answer carries on stdout; a larger one
// goes into sets.jsonl.
const inlineUnits = 2000

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return plugin.Serve(args, stdin, stdout, stderr, map[string]plugin.Handler{
		plugin.CallDescribe: func([]byte) (any, error) { return describe(), nil },
		plugin.CallDetect:   detect,
		plugin.CallUnits:    func(in []byte) (any, error) { return units(in, stderr) },
	})
}

func describe() plugin.Description {
	return plugin.Description{
		Protocol:     plugin.Protocol,
		Kind:         plugin.KindSBOM,
		Name:         "locale-sbom",
		Version:      Version,
		Namespaces:   []string{catalog.Namespace},
		Capabilities: []string{plugin.CallDetect, plugin.CallUnits},
		Narrowings: []plugin.Narrowing{{
			Name:  catalog.Narrowing,
			OffAt: 3,
			Note: "a slice that depends on a catalog file depends on the keys it was seen to look up, in each language, " +
				"misses included; it misses text a test takes from the file other than through a recorded lookup",
		}},
		Candidates: catalog.Candidates,
		Inputs:     map[string]string{plugin.CallDetect: plugin.PerFile, plugin.CallUnits: plugin.PerSet},
	}
}

// detect claims every catalog file. A claim's anchor is the catalog's own
// unit, which a recorder adds whenever it ran for that catalog, so the core
// may replace a slice's path to the file by the keys the slice looked up.
func detect(in []byte) (any, error) {
	var req plugin.DetectRequest
	if err := json.Unmarshal(in, &req); err != nil {
		return nil, fmt.Errorf("reading the request: %w", err)
	}

	claims := []plugin.Claim{}

	for _, p := range req.Paths {
		ok, err := catalog.Claimable(req.Tree, p)
		if err != nil {
			return nil, err
		}

		if ok {
			f, _ := catalog.Locate(p)
			claims = append(claims, plugin.Claim{
				Path: p, Role: roleCatalog, ByUnits: catalog.Narrowing, Anchor: catalog.ID(f.Catalog),
			})
		}
	}

	return plugin.DetectAnswer{Claims: claims}, nil
}

func units(in []byte, stderr io.Writer) (any, error) {
	var req plugin.TreeRequest
	if err := json.Unmarshal(in, &req); err != nil {
		return nil, fmt.Errorf("reading the request: %w", err)
	}

	files := map[string]catalog.File{}
	byCat := map[string][]catalog.File{}

	for _, c := range req.Claimed {
		f, ok := catalog.Locate(c.Path)
		if c.Role != roleCatalog || !ok {
			continue
		}

		files[c.Path] = f
		byCat[f.Catalog] = append(byCat[f.Catalog], f)
	}

	asked := req.Paths
	if len(asked) == 0 {
		asked = slices.Sorted(maps.Keys(files))
	}

	var (
		sets    []plugin.Set
		cats    = map[string]bool{}
		gone    []string
		cantFor []string
	)

	for _, p := range asked {
		f, claimed := files[p]
		if !claimed {
			gone = append(gone, p)

			if lf, ok := catalog.Locate(p); ok {
				cats[lf.Catalog] = true
			}

			continue
		}

		cats[f.Catalog] = true

		s, err := fileSet(req.Tree, f)
		if errors.Is(err, errUnreadable) {
			cantFor = append(cantFor, err.Error())

			continue
		}

		if err != nil {
			return nil, err
		}

		sets = append(sets, s)
	}

	if len(cantFor) > 0 {
		return nil, cantTell(stderr, cantFor)
	}

	for _, cat := range slices.Sorted(maps.Keys(cats)) {
		sets = append(sets, catalogSet(cat, byCat[cat]))
	}

	if len(gone) > 0 {
		slices.Sort(gone)
		sets = append(sets, plugin.Set{Group: gone[0], Inputs: []string{}, Absent: gone, Units: []plugin.Unit{}})
	}

	slices.SortFunc(sets, func(a, b plugin.Set) int { return strings.Compare(a.Group, b.Group) })

	return answer(req.Work, sets)
}

var errUnreadable = errors.New("can't read the catalog")

func cantTell(stderr io.Writer, whys []string) error {
	for _, why := range whys {
		if _, err := fmt.Fprintln(stderr, "locale-sbom: "+why); err != nil {
			break
		}
	}

	return fmt.Errorf("%w: %s", plugin.ErrCantTell, strings.Join(whys, "; "))
}

func fileSet(tree string, f catalog.File) (plugin.Set, error) {
	b, err := os.ReadFile(filepath.Join(tree, filepath.FromSlash(f.Path))) //nolint:gosec // a claimed file of the tree
	if err != nil {
		return plugin.Set{}, fmt.Errorf("reading %s: %w", f.Path, err)
	}

	top, err := catalog.Parse(b)
	if err != nil {
		return plugin.Set{}, fmt.Errorf("%w %s: %w", errUnreadable, f.Path, err)
	}

	s := plugin.Set{Group: f.Path, Inputs: []string{f.Path}}
	s.Units = append(s.Units, plugin.Unit{
		ID: catalog.NSID(f.Catalog, f.Lang, f.NS), Group: catalog.Group(f.Catalog, f.Lang, f.NS, ""),
		Digest: catalog.PresentDigest(), File: f.Path, Kind: catalog.KindNamespace, Executable: true,
		Label: f.Lang + "/" + f.NS + " is there",
	})

	for _, n := range catalog.Nodes(top) {
		// a key's id reads as its label; only the whole namespace gets one
		kind, label := catalog.KindKey, ""
		if !n.Leaf {
			kind = catalog.KindObject
		}

		if n.Key == "" {
			label = f.Lang + "/" + f.NS + ", whole"
		}

		s.Units = append(s.Units, plugin.Unit{
			ID: catalog.KeyID(f.Catalog, f.Lang, f.NS, n.Key), Group: catalog.Group(f.Catalog, f.Lang, f.NS, n.Key),
			Digest: n.Digest, File: f.Path, Kind: kind, Executable: true, Label: label,
		})
	}

	return s, nil
}

// catalogSet's inputs are every file of the catalog, so a file added or gone
// asks again.
func catalogSet(cat string, files []catalog.File) plugin.Set {
	s := plugin.Set{Group: cat, Inputs: []string{}, Units: []plugin.Unit{}}

	langs := map[string]bool{}

	for _, f := range files {
		s.Inputs = append(s.Inputs, f.Path)
		langs[f.Lang] = true
	}

	slices.Sort(s.Inputs)

	if len(files) == 0 {
		return s
	}

	s.Units = append(s.Units, plugin.Unit{
		ID: catalog.ID(cat), Group: cat, Digest: catalog.PresentDigest(),
		Kind: catalog.KindCatalog, Executable: true, Label: "the catalog " + cat,
	})

	for _, l := range slices.Sorted(maps.Keys(langs)) {
		s.Units = append(s.Units, plugin.Unit{
			ID: catalog.LangID(cat, l), Group: cat, Digest: catalog.PresentDigest(),
			Kind: catalog.KindLanguage, Executable: true, Label: l + " is there",
		})
	}

	return s
}

func answer(work string, sets []plugin.Set) (any, error) {
	n := 0
	for _, s := range sets {
		n += len(s.Units)
	}

	if n <= inlineUnits || work == "" {
		return plugin.UnitsAnswer{Sets: sets}, nil
	}

	f, err := os.Create(filepath.Join(work, "sets.jsonl")) //nolint:gosec // the call's own work directory
	if err != nil {
		return nil, fmt.Errorf("writing the answer: %w", err)
	}

	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)

	for _, s := range sets {
		if err := enc.Encode(s); err != nil {
			_ = f.Close() //nolint:errcheck // the write already failed

			return nil, fmt.Errorf("writing the answer: %w", err)
		}
	}

	if err := errors.Join(w.Flush(), f.Close()); err != nil {
		return nil, fmt.Errorf("writing the answer: %w", err)
	}

	return plugin.UnitsAnswer{Files: []string{"sets.jsonl"}}, nil
}
