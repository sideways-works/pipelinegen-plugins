package graph

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sideways-works/pipelinegen-plugins/sbom/go/decls"
	"github.com/sideways-works/pipelinegen-plugins/sbom/go/goid"
)

// NamesGroup is the group of the set that holds every link depending on N,
// the method names some interface or lookup by name can reach. N is a fact of
// the whole program, so the set's inputs are every Go file, go.mod, go.work
// and embedded file, read from syntax.
const NamesGroup = "go:method-names"

// AllMethodsNode is the junction to every type's whole method set: what
// reaches it can look methods up by reflection in ways no name tells.
const AllMethodsNode = "node:go:all-methods"

// Names is N and what the set holding the links that depend on it needs.
type Names struct {
	// Names are every interface method name the linked program declares, every
	// constant name a lookup by name asks for, and every name after a dot in
	// template text.
	Names map[string]bool
	// Types are the tree's types with methods, outside the tests, as
	// go-sbom lists them.
	Types []TypeMethods
	// Hands are, per junction, the packages outside the tree its binaries link
	// directly that reach methods in ways no name tells: what the tree hands to
	// them owes its whole method set.
	Hands map[string][]string
	// Wide are, per package outside the tree in Hands, the places that
	// make it so, for people.
	Wide map[string][]string
	// methods are, per package in Hands, the names of the methods it
	// declares: an interface method the tree calls may run one of them.
	methods map[string][]string
	// tests are, per directory with a test binary, the packages of Hands
	// the binary links through any package it links: what the directory's
	// test files hand to them, or to a method they declare, counts there.
	tests map[string][]string
	// testSels are, per directory, every name after a dot in its test
	// files: an interface method its tests call is one of them.
	testSels map[string]map[string]bool
	bins     []testBin
	Inputs   []string
	// Uncacheable says why the answer depends on the machine (a
	// dependency's source missing from the module cache), when it does.
	Uncacheable string
}

type testBin struct {
	dir, pkg string
	deps     []string
}

// TypeMethods is one type with methods: its units and its methods'
// signature units by method name.
type TypeMethods struct {
	Type, Decl string
	Sigs       map[string]string
}

type NamesOptions struct {
	// Modules are the directories of the modules to read; none reads
	// every go.mod of the tree.
	Modules []string
	Build   decls.Build
	// Cache is a directory kept between runs: what a dependency's source
	// holds is kept there by module version, and the standard library's by
	// Go version. Empty keeps nothing.
	Cache string
}

func ReadNames(ctx context.Context, root string, o NamesOptions) (*Names, error) {
	root, err := decls.Root(root)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLoad, err)
	}

	mods, err := findModules(root)
	if err != nil {
		return nil, err
	}

	// the predeclared error is the one interface no source declares
	n := &Names{
		Names: map[string]bool{"Error": true}, Hands: map[string][]string{}, Wide: map[string][]string{},
		methods: map[string][]string{}, tests: map[string][]string{}, testSels: map[string]map[string]bool{},
	}
	bctx := o.Build.Context()
	inputs := map[string]bool{}

	var read []string

	for _, m := range mods {
		if len(o.Modules) > 0 && !slices.Contains(o.Modules, m.Dir) {
			continue
		}

		read = append(read, m.Dir)
		inputs[path.Join(m.Dir, goMod)] = true

		if w := workOf(root, m.Dir); w != "" {
			inputs[w] = true
		}

		dirs, err := decls.Dirs(root, m.Dir)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrLoad, err)
		}

		for _, dir := range dirs {
			if err := n.treeDir(root, dir, &bctx, inputs); err != nil {
				return nil, err
			}
		}
	}

	// every module's own files first: a template parse of text the scan
	// can't read anywhere in the tree makes the template packages read
	// methods in ways no name tells for every module
	for _, md := range read {
		if err := n.dependencies(ctx, root, mods, md, o); err != nil {
			return nil, err
		}
	}

	n.testHands(mods)

	n.Inputs = sortedKeys(inputs)

	slices.SortFunc(n.Types, func(a, b TypeMethods) int { return strings.Compare(a.Type, b.Type) })

	return n, nil
}

const (
	goMod  = "go.mod"
	goWork = "go.work"
)

// treeDir reads one package directory of the tree: its types with methods,
// keyed as go-sbom keys them, and the names every file adds, files the build
// leaves out included.
func (n *Names) treeDir(root, dir string, bctx *build.Context, inputs map[string]bool) error {
	abs := filepath.Join(root, filepath.FromSlash(dir))

	entries, err := os.ReadDir(abs)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrLoad, err)
	}

	fset := token.NewFileSet()
	broken := false

	var (
		kept  []goid.Decl
		files []fileFacts
		tests []bool
	)

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}

		inputs[path.Join(dir, name)] = true

		f, err := parser.ParseFile(fset, filepath.Join(abs, name), nil, parser.ParseComments|parser.SkipObjectResolution)

		match, merr := bctx.MatchFile(abs, name)
		if merr != nil {
			match = false
		}

		if err != nil {
			// go-sbom lists a package with a file that doesn't parse by its
			// files alone: no type of it has units of its own
			broken = broken || match

			continue
		}

		// every literal of the tree's own files counts, whether or not its
		// package parses templates: a constant can be handed to Parse from
		// another package
		facts := scanFile(f)
		n.add(facts.names)
		n.add(facts.literals)

		for _, s := range facts.sites {
			if v, ok := facts.consts[s.ident]; ok && s.ident != "" {
				n.add(v)
			}
		}

		for _, p := range embedPatterns(f) {
			for _, file := range embedded(abs, p) {
				rel := filepath.ToSlash(relPath(root, file))
				inputs[rel] = true

				n.add(textNames(file))
			}
		}

		if match {
			kept = append(kept, goid.File(dir, name, f)...)
		}

		files = append(files, facts)
		tests = append(tests, strings.HasSuffix(name, "_test.go"))

		if strings.HasSuffix(name, "_test.go") {
			n.selectors(dir, f)
		}
	}

	if !broken {
		n.Types = append(n.Types, typesOf(dir, kept)...)
	}

	n.treeTemplates(dir, fset, files, tests)

	return nil
}

func (n *Names) treeTemplates(dir string, fset *token.FileSet, files []fileFacts, tests []bool) {
	for i, f := range files {
		pf := joinFiles(append([]fileFacts{{consts: f.consts, embeds: f.embeds, parses: f.parses, templates: f.templates}},
			withoutParses(files, i)...))

		from := GroupNode(dir)
		if tests[i] {
			from = TestNode(dir)
		}

		for _, at := range pf.sites {
			for _, tp := range []string{textTemplate, htmlTemplate} {
				n.Hands[from] = appendNew(n.Hands[from], tp)
				n.Wide[tp] = appendNew(n.Wide[tp], dir+"/"+siteOf(fset, at))
			}
		}
	}
}

func (n *Names) selectors(dir string, f *ast.File) {
	if n.testSels[dir] == nil {
		n.testSels[dir] = map[string]bool{}
	}

	ast.Inspect(f, func(x ast.Node) bool {
		if sel, ok := x.(*ast.SelectorExpr); ok {
			n.testSels[dir][sel.Sel.Name] = true
		}

		return true
	})
}

func (n *Names) testHands(mods modules) {
	for _, b := range n.bins {
		in := map[string]bool{}

		for _, p := range n.Hands[TestNode(b.dir)] {
			in[p] = true
		}

		for _, d := range b.deps {
			base, variant, _ := strings.Cut(d, " ")

			dir, inTree := mods.dirOf(strings.TrimSuffix(base, "_test"))
			if !inTree {
				continue
			}

			j := GroupNode(dir)
			if variant != "" && (base == b.pkg || base == b.pkg+"_test") {
				j = TestNode(dir)
			}

			for _, p := range n.Hands[j] {
				in[p] = true
			}
		}

		if len(in) > 0 {
			n.tests[b.dir] = sortedKeys(in)
		}
	}

	n.bins = nil
}

func withoutParses(files []fileFacts, skip int) []fileFacts {
	out := make([]fileFacts, 0, len(files))

	for i, f := range files {
		if i != skip {
			out = append(out, fileFacts{consts: f.consts, embeds: f.embeds})
		}
	}

	return out
}

func typesOf(dir string, ds []goid.Decl) []TypeMethods {
	has := map[goid.ID]bool{}
	for _, d := range ds {
		has[d.ID] = true
	}

	byType := map[goid.ID]*TypeMethods{}

	for _, d := range ds {
		fd, ok := d.Node.(*ast.FuncDecl)
		if !ok || d.Recv == "" {
			continue
		}

		typ := goid.New(dir, goid.Type, d.Recv)
		if !has[typ] {
			// a test type, or one the build leaves out: no signature units
			continue
		}

		tm := byType[typ]
		if tm == nil {
			tm = &TypeMethods{Type: string(typ), Decl: string(goid.New(dir, goid.TypeDecl, d.Recv)), Sigs: map[string]string{}}
			byType[typ] = tm
		}

		tm.Sigs[fd.Name.Name] = string(goid.New(dir, goid.MethodSig, d.ID.Name()))
	}

	out := make([]TypeMethods, 0, len(byType))
	for _, typ := range sortedKeys(byType) {
		out = append(out, *byType[typ])
	}

	return out
}

func (n *Names) add(names []string) {
	for _, m := range names {
		n.Names[m] = true
	}
}

// dotNames are the exported names after a dot in a text: what a template
// action such as {{.Total}} or {{$x.Owner.Name}} looks up, by reflection,
// as a field or a method. Only exported methods can be looked up.
func dotNames(s string) []string {
	var out []string

	for i := 0; i < len(s); i++ {
		if s[i] != '.' {
			continue
		}

		r, size := utf8.DecodeRuneInString(s[i+1:])
		if !unicode.IsUpper(r) {
			continue
		}

		j := i + 1 + size
		for j < len(s) {
			r, size := utf8.DecodeRuneInString(s[j:])
			if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				break
			}

			j += size
		}

		out = append(out, s[i+1:j])
		i = j - 1
	}

	return out
}

func textNames(file string) []string {
	b, err := os.ReadFile(file) //nolint:gosec // a file a package of the tree embeds
	if err != nil || bytes.IndexByte(b, 0) >= 0 {
		return nil
	}

	return dotNames(string(b))
}

func embedPatterns(f *ast.File) []string {
	var out []string

	for _, g := range f.Comments {
		for _, c := range g.List {
			rest, ok := strings.CutPrefix(c.Text, "//go:embed ")
			if !ok {
				continue
			}

			for p := range strings.FieldsSeq(rest) {
				out = append(out, strings.TrimPrefix(strings.Trim(p, "\"`"), "all:"))
			}
		}
	}

	return out
}

// embedded are the files a pattern names under dir, a directory's whole
// content included: more than the go command embeds (it leaves out names
// starting with . or _), never less.
func embedded(dir, pattern string) []string {
	matches, _ := filepath.Glob(filepath.Join(dir, filepath.FromSlash(pattern))) //nolint:errcheck // a bad pattern fails the build; it names nothing here

	var out []string

	for _, m := range matches {
		_ = filepath.WalkDir(m, func(p string, e fs.DirEntry, err error) error { //nolint:errcheck // the callback never fails
			if err == nil && !e.IsDir() {
				out = append(out, p)
			}

			return nil
		})
	}

	return out
}

type listedDep struct {
	ImportPath string   `json:"ImportPath"` //nolint:tagliatelle // the go command's names
	Dir        string   `json:"Dir"`        //nolint:tagliatelle // as above
	Name       string   `json:"Name"`       //nolint:tagliatelle // as above
	ForTest    string   `json:"ForTest"`    //nolint:tagliatelle // as above
	Standard   bool     `json:"Standard"`   //nolint:tagliatelle // as above
	GoFiles    []string `json:"GoFiles"`    //nolint:tagliatelle // as above
	CgoFiles   []string `json:"CgoFiles"`   //nolint:tagliatelle // as above
	EmbedFiles []string `json:"EmbedFiles"` //nolint:tagliatelle // as above
	Imports    []string `json:"Imports"`    //nolint:tagliatelle // as above
	Deps       []string `json:"Deps"`       //nolint:tagliatelle // as above
	Module     *struct {
		Path    string `json:"Path"`    //nolint:tagliatelle // as above
		Version string `json:"Version"` //nolint:tagliatelle // as above
		Replace *struct {
			Path    string `json:"Path"`    //nolint:tagliatelle // as above
			Version string `json:"Version"` //nolint:tagliatelle // as above
		} `json:"Replace"` //nolint:tagliatelle // as above
	} `json:"Module"` //nolint:tagliatelle // as above
}

type depFacts struct {
	Names []string `json:"names"`
	// Sites are the places that enumerate methods, look them up by a
	// computed name or parse a template that isn't a literal.
	Sites   []string `json:"sites"`
	Methods []string `json:"methods,omitempty"`

	// Missing says the package's source can't be read.
	Missing string `json:"missing,omitempty"`
}

const reflectPkg = "reflect"

// ownReflection are the packages whose own lookups are the mechanism the
// rules already account for, not a lookup of their own: reflect itself,
// the templates, whose names come from their text, and go/types, whose
// Method(i) and NumMethod are on types.Named and types.Interface.
var ownReflection = map[string]bool{
	reflectPkg: true, "internal/reflectlite": true, textTemplate: true, htmlTemplate: true,
	"go/types": true, "golang.org/x/tools/internal/typesinternal": true, "internal/abi": true,
}

// dependencies reads N from the sources of every package outside the tree
// that the module's binaries and test binaries link.
func (n *Names) dependencies(ctx context.Context, root string, mods modules, md string, o NamesOptions) error {
	all, err := listDeps(ctx, filepath.Join(root, md), o.Build)
	if err != nil {
		// a module the go command can't list is bound wide by its own
		// package sets, every package of it on its own
		return nil //nolint:nilerr // the module's own sets carry the failure
	}

	goVersion, err := GoVersion(ctx, filepath.Join(root, md), o.Build)
	if err != nil {
		return err
	}

	byPath := map[string]listedDep{}
	facts := map[string]depFacts{}

	for _, l := range all {
		base, _, _ := strings.Cut(l.ImportPath, " ")
		if _, ok := byPath[base]; !ok || !strings.Contains(l.ImportPath, " ") {
			byPath[base] = l
		}

		if pkg, ok := strings.CutSuffix(base, ".test"); ok {
			if dir, inTree := mods.dirOf(pkg); inTree && !l.Standard {
				n.bins = append(n.bins, testBin{dir: dir, pkg: pkg, deps: l.Deps})
			}

			continue
		}

		if _, inTree := mods.dirOf(base); inTree && !l.Standard {
			continue
		}

		if _, done := facts[base]; done {
			continue
		}

		f := depOf(l, goVersion, o.Cache)
		facts[base] = f
		n.add(f.Names)

		if f.Missing != "" && n.Uncacheable == "" {
			n.Uncacheable = f.Missing
		}
	}

	n.wideLinks(all, byPath, facts, mods)

	return nil
}

// wideLinks notes, for each package of the tree and each test binary, the
// packages outside the tree it links directly that reach methods in ways no
// name tells.
func (n *Names) wideLinks(all []listedDep, byPath map[string]listedDep, facts map[string]depFacts, mods modules) {
	reaches := reachingOf(byPath, facts, len(n.Wide[textTemplate])+len(n.Wide[htmlTemplate]) > 0)

	for p, f := range facts {
		for _, s := range f.Sites {
			n.Wide[p] = appendNew(n.Wide[p], s)
		}

		if f.Missing != "" {
			n.Wide[p] = appendNew(n.Wide[p], f.Missing)
		}
	}

	for _, l := range all {
		from := junctionOf(l, mods)
		if from == "" {
			continue
		}

		seen := map[string]bool{}

		var visit func(p string)

		visit = func(p string) {
			p, _, _ = strings.Cut(p, " ")
			if _, ok := facts[p]; !ok || seen[p] {
				return
			}

			seen[p] = true

			if reaches(p) {
				n.Hands[from] = appendNew(n.Hands[from], p)
				n.methods[p] = facts[p].Methods
			}

			for _, im := range byPath[p].Imports {
				visit(im)
			}
		}

		for _, im := range l.Imports {
			visit(im)
		}
	}

	for _, tp := range []string{textTemplate, htmlTemplate} {
		if f, ok := facts[tp]; ok {
			n.methods[tp] = f.Methods
		}
	}
}

// junctionOf is the junction a listed package of the tree is linked into
// binaries by: its group, or its test binary's node for a test variant;
// empty for a package outside the tree.
func junctionOf(l listedDep, mods modules) string {
	base, _, _ := strings.Cut(l.ImportPath, " ")

	dir, inTree := mods.dirOf(strings.TrimSuffix(strings.TrimSuffix(base, ".test"), "_test"))

	switch {
	case !inTree || l.Standard:
		return ""
	case strings.HasSuffix(base, ".test"), l.ForTest != "", strings.HasSuffix(base, "_test"):
		return TestNode(dir)
	}

	return GroupNode(dir)
}

// reachingOf tells, for a package outside the tree, whether it reads
// methods in ways no name tells, or imports one that does.
//
// With templates set, the tree parses template text the scan can't read
// somewhere: a template package may then execute it on what it is handed,
// whoever hands it over, and so does a package that imports one.
func reachingOf(byPath map[string]listedDep, facts map[string]depFacts, templates bool) func(string) bool {
	reaching := map[string]bool{}

	var reaches func(p string, seen map[string]bool) bool

	reaches = func(p string, seen map[string]bool) bool {
		if r, done := reaching[p]; done {
			return r
		}

		f, ok := facts[p]
		if !ok || seen[p] {
			return false // the tree's own (its own junctions say it), or a cycle
		}

		seen[p] = true
		r := len(f.Sites) > 0 || f.Missing != "" || (templates && (p == textTemplate || p == htmlTemplate))

		for _, im := range byPath[p].Imports {
			im, _, _ = strings.Cut(im, " ")
			r = reaches(im, seen) || r
		}

		reaching[p] = r

		return r
	}

	return func(p string) bool { return reaches(p, map[string]bool{}) }
}

func listDeps(ctx context.Context, dir string, b decls.Build) ([]listedDep, error) {
	args := append([]string{"list", "-deps", "-test", "-e",
		"-json=ImportPath,Dir,Name,ForTest,Standard,GoFiles,CgoFiles,EmbedFiles,Imports,Deps,Module"}, BuildFlags(b)...)

	cmd := exec.CommandContext(ctx, "go", append(args, "./...")...) //nolint:gosec // the go command, with the build's flags
	cmd.Dir = dir
	cmd.Env = Env(b)

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: go list in %s: %w: %s", ErrModule, dir, err, stderr.String())
	}

	var all []listedDep

	dec := json.NewDecoder(bytes.NewReader(out))

	for {
		var l listedDep
		if err := dec.Decode(&l); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("%w: go list in %s: %w", ErrModule, dir, err)
		}

		all = append(all, l)
	}

	return all, nil
}

// factsVersion keys the cached facts by what they hold.
const factsVersion = "6/"

// depOf reads one package outside the tree, from the cache when its source
// can't change: a module version's, or the standard library's of one Go
// version. A module replaced by a directory is read every time.
func depOf(l listedDep, goVersion, cache string) depFacts {
	base, _, _ := strings.Cut(l.ImportPath, " ")

	key := ""

	switch {
	case l.Standard:
		key = "std@" + goVersion + "/" + base
	case l.Module != nil && l.Module.Replace != nil && l.Module.Replace.Version != "":
		key = l.Module.Replace.Path + "@" + l.Module.Replace.Version + "/" + base
	case l.Module != nil && l.Module.Replace == nil && l.Module.Version != "":
		key = l.Module.Path + "@" + l.Module.Version + "/" + base
	}

	file := ""

	if key != "" && cache != "" {
		sum := sha256.Sum256([]byte(factsVersion + key))
		file = filepath.Join(cache, "names", hex.EncodeToString(sum[:])+".json")

		if b, err := os.ReadFile(file); err == nil { //nolint:gosec // the plugin's own cache
			var f depFacts
			if json.Unmarshal(b, &f) == nil {
				return f
			}
		}
	}

	f := scanDep(l, base)

	if file != "" && f.Missing == "" {
		if b, err := json.Marshal(f); err == nil && os.MkdirAll(filepath.Dir(file), 0o750) == nil {
			_ = os.WriteFile(file, b, 0o600) //nolint:errcheck // a cache: the next run reads the source again
		}
	}

	return f
}

func scanDep(l listedDep, base string) depFacts {
	files := slices.Concat(l.GoFiles, l.CgoFiles)
	if l.Dir == "" || (len(files) == 0 && l.Name != "") {
		return depFacts{Missing: "its source is not in the module cache"}
	}

	fset := token.NewFileSet()
	ffs := make([]fileFacts, 0, len(files))
	methods := map[string]bool{}

	for _, name := range files {
		f, err := parser.ParseFile(fset, filepath.Join(l.Dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return depFacts{Missing: "its source can't be read: " + err.Error()}
		}

		ffs = append(ffs, scanFile(f))

		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv != nil {
				methods[fd.Name.Name] = true
			}
		}
	}

	pf := joinFiles(ffs)
	names := map[string]bool{}

	for _, m := range pf.names {
		names[m] = true
	}

	if pf.templates {
		// what the package parses may be any of its literals, or a file it
		// embeds
		for _, m := range pf.literals {
			names[m] = true
		}

		for _, e := range l.EmbedFiles {
			for _, m := range textNames(filepath.Join(l.Dir, e)) {
				names[m] = true
			}
		}
	}

	out := depFacts{Names: sortedKeys(names), Methods: sortedKeys(methods)}

	if !ownReflection[base] {
		for _, at := range pf.sites {
			out.Sites = append(out.Sites, siteOf(fset, at))
		}
	}

	slices.Sort(out.Sites)

	return out
}

// siteOf names a place by its file's base name and line: the module cache's
// directory is the machine's.
func siteOf(fset *token.FileSet, at token.Pos) string {
	p := fset.Position(at)

	return filepath.Base(p.Filename) + ":" + strconv.Itoa(p.Line)
}

// Set is the set of every link that depends on N.
func (n *Names) Set() LinkSet {
	s := LinkSet{Group: NamesGroup, Inputs: slices.Clone(n.Inputs)}

	for _, tm := range n.Types {
		for _, m := range sortedKeys(tm.Sigs) {
			if n.Names[m] {
				s.Links = append(s.Links, Link{From: tm.Decl, To: tm.Sigs[m], Kind: KindBind})
			}
		}

		// a type whose package the binary doesn't link has no values there
		all := []Link{junctionLink(AllMethodsNode, tm.Type), junctionLink(AllMethodsNode, tm.Decl)}
		all[0].IfLinked, all[1].IfLinked = true, true
		s.Links = append(s.Links, all...)
	}

	handed := map[string]bool{}

	for _, from := range sortedKeys(n.Hands) {
		for _, p := range n.Hands[from] {
			s.Links = append(s.Links, junctionLink(from, HandedTo(p)))
			handed[p] = true

			// what the package's own code hands to it
			if dir, ok := strings.CutPrefix(from, "group:"); ok {
				s.Links = append(s.Links, junctionLink(from, inGroup(HandedTo(p), dir)))
			}
		}
	}

	if len(handed) > 0 {
		s.Links = append(s.Links, junctionLink(assertsFunc, handedByFunc))
	}

	// what a test file hands over counts in its own test binary when the
	// binary links such a package: to it, and to a method it declares that
	// the tests call by name
	for _, dir := range sortedKeys(n.tests) {
		test := TestNode(dir)
		called := map[string]bool{}

		for _, p := range n.tests[dir] {
			s.Links = append(s.Links, junctionLink(test, inTest(HandedTo(p), dir)))

			for _, m := range n.methods[p] {
				if n.testSels[dir][m] {
					called[m] = true
				}
			}
		}

		for _, m := range sortedKeys(called) {
			s.Links = append(s.Links, junctionLink(test, inTest(handedByName(m), dir)))
		}
	}

	// what the tree hands to an interface's method may land in such a
	// package: one of its types may implement the method (a func value it
	// made is linked where the tree's code obtains one)
	for _, p := range sortedKeys(handed) {
		for _, m := range n.methods[p] {
			s.Links = append(s.Links, junctionLink(HandedTo(p), handedByName(m)))
		}
	}

	slices.SortFunc(s.Links, compareLinks)

	return s
}

// has tells whether a method name is in N. Without N every name is: the
// signatures all bind, as the whole type would.
func (n *Names) has(name string) bool {
	return n == nil || n.Names[name]
}

func methodName(d *Decl) string {
	_, name, _ := strings.Cut(d.ID.Name(), ".")
	name, _, _ = strings.Cut(name, "@")

	return name
}

// SetNames gives the tree N, which bind reads: the signatures a type's
// declaration binds, and the junctions that bind whole method sets.
func (t *Tree) SetNames(n *Names) {
	t.names = n
}

func (t *Tree) buildJunctions() {
	if t.junctions != nil {
		return
	}

	t.junctions = map[string][]string{}

	sets := [][]Link{}
	for _, ls := range t.extra {
		sets = append(sets, ls)
	}

	if t.names != nil {
		sets = append(sets, t.names.Set().Links)
	}

	for _, ls := range sets {
		for _, l := range ls {
			if l.Kind == KindFollow {
				t.junctions[l.From] = append(t.junctions[l.From], l.To)
			}
		}
	}
}

// wholeSets are the types a slice binds with their whole method set:
// those the junctions its binaries link reach, from a package it links or
// from a test binary it runs, as the walk follows the reach links.
func (t *Tree) wholeSets(linked, testDirs map[string]bool, o options) []int32 {
	if !o.ifaceMethods {
		return nil
	}

	t.buildJunctions()

	seen := map[string]bool{}

	var queue []string

	for dir := range linked {
		queue = append(queue, GroupNode(dir))
	}

	for dir := range testDirs {
		queue = append(queue, TestNode(dir))
	}

	var out []int32

	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]

		if seen[n] {
			continue
		}

		seen[n] = true

		if i, ok := t.byID[goid.ID(n)]; ok && t.Decls[i].ID.Kind() == goid.Type {
			out = append(out, i)

			continue
		}

		queue = append(queue, t.junctions[n]...)
	}

	slices.Sort(out)

	return slices.Compact(out)
}
