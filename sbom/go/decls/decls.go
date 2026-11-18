// Package decls lists what a Go package declares as units, each hashed so its
// digest changes exactly when what the compiler reads of it can. A package is
// read from its files alone, without a type check.
package decls

import (
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/sideways-works/pipelinegen-plugins/sbom/go/goid"
)

type Unit struct {
	ID     goid.ID
	File   string // from the repository's root
	Digest string
	// Executable is true for what coverage can see run.
	Executable    bool
	Empty         bool
	Line, EndLine int
	// DigestFrom, when set, makes the unit change exactly when those units do.
	DigestFrom []string
}

// DigestFrom gives the declarations whose IDs match a pattern their digest
// from other units. A pattern is an ID where * stands for any run of
// characters: "go:*#var:swaggerSpec".
type DigestFrom struct {
	Units []string `json:"units"`
	From  []string `json:"from"`
}

type Config struct {
	DigestFrom []DigestFrom `json:"digest_from"`
	Build      Build        `json:"build"`
}

// Build says which files the build constraints keep. It is fixed rather than
// read from the machine, so every machine gives the same answer.
type Build struct {
	// GOOS and GOARCH default to linux and amd64.
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
	// Cgo is CGO_ENABLED; off unless set.
	Cgo bool `json:"cgo"`
	// Tags are the build tags; nil is DefaultTags.
	Tags []string `json:"tags"`
}

// DefaultTags are the build tags of a repository that names none: the
// coverage build's, so the file linking the cover package is read like any
// other.
var DefaultTags = []string{"pipelinegen_cover"}

func (b Build) Context() build.Context {
	ctx := build.Default
	ctx.GOOS, ctx.GOARCH, ctx.CgoEnabled = "linux", "amd64", b.Cgo

	if b.GOOS != "" {
		ctx.GOOS = b.GOOS
	}

	if b.GOARCH != "" {
		ctx.GOARCH = b.GOARCH
	}

	ctx.BuildTags = b.Tags
	if b.Tags == nil {
		ctx.BuildTags = DefaultTags
	}

	return ctx
}

func (c Config) from(id goid.ID) []string {
	for _, r := range c.DigestFrom {
		for _, pat := range r.Units {
			if matchID(pat, string(id)) {
				return r.From
			}
		}
	}

	return nil
}

func matchID(pat, id string) bool {
	parts := strings.Split(pat, "*")
	if len(parts) == 1 {
		return pat == id
	}

	if !strings.HasPrefix(id, parts[0]) {
		return false
	}

	rest := id[len(parts[0]):]

	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(rest, p)
		if i < 0 {
			return false
		}

		rest = rest[i+len(p):]
	}

	return strings.HasSuffix(rest, parts[len(parts)-1])
}

type Package struct {
	Dir   string
	Units []Unit
	// Whole says why the package can't be bound by declaration, when it can't.
	Whole string
	// Inputs are the package's Go files and the files its embeds match.
	Inputs []string
	// Private are, per type, the signature units of unexported methods only a
	// call naming them can reach. They stay out of the type's digest; while
	// unexported-methods is off, the type links to them instead.
	Private map[goid.ID][]goid.ID
	// Unparsed is the parse error of a file the build keeps. Each Go file of the
	// package is then one unit by its content, and the package is whole.
	Unparsed string

	methods []pending
	shared  map[string]bool
	api     map[goid.ID]string
	name    string
}

var ErrRead = errors.New("read package")

// Read reads the package in dir (relative to root) and its test files. A file
// the build constraints leave out is one unit by its content and, outside a
// test file, makes the package whole, since another build compiles it.
func Read(root, dir string) (*Package, error) {
	return ReadWith(root, dir, Config{})
}

func ReadWith(root, dir string, cfg Config) (*Package, error) {
	root, err := Root(root)
	if err != nil {
		return nil, fmt.Errorf("%w %s: %w", ErrRead, dir, err)
	}

	return read(root, dir, cfg)
}

func (p *Package) Name() string { return p.name }

func read(root, dir string, cfg Config) (*Package, error) {
	abs := filepath.Join(root, dir)

	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("%w %s: %w", ErrRead, dir, err)
	}

	p := &Package{Dir: filepath.ToSlash(dir)}
	fset := token.NewFileSet()
	bctx := cfg.Build.Context()

	var files []*ast.File

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			continue
		}

		switch filepath.Ext(name) {
		case ".go":
		case ".s", ".c", ".cc", ".cpp", ".h", ".syso":
			p.whole("non-Go files (" + name + ")")

			continue
		default:
			continue
		}

		p.Inputs = append(p.Inputs, filepath.ToSlash(filepath.Join(dir, name)))

		if ok, err := bctx.MatchFile(abs, name); err != nil || !ok {
			if err := p.excluded(root, dir, name); err != nil {
				return nil, err
			}

			continue
		}

		f, err := parser.ParseFile(fset, filepath.Join(abs, name), nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return unparsed(root, dir, entries, p, err)
		}

		files = append(files, f)
	}

	shadowed := shadows(files)
	p.shared = sharedNames(files)

	for _, f := range files {
		p.addFile(root, fset, f, shadowed)
	}

	p.foldMethodSets()
	p.addAPI()

	for i, u := range p.Units {
		if from := cfg.from(u.ID); from != nil {
			p.Units[i].Digest, p.Units[i].DigestFrom = "", from
		}
	}

	slices.Sort(p.Inputs)
	p.Inputs = slices.Compact(p.Inputs)

	return p, nil
}

// unparsed answers a package with a file that doesn't parse: one unit per Go
// file and the package whole, since no declaration can be told apart.
func unparsed(root, dir string, entries []os.DirEntry, p *Package, perr error) (*Package, error) {
	msg := perr.Error()
	if rel, ok := strings.CutPrefix(msg, filepath.Join(root, dir)+string(filepath.Separator)); ok {
		msg = rel
	}

	out := &Package{Dir: p.Dir, Inputs: p.Inputs, Unparsed: msg}
	out.whole("a file doesn't parse: " + msg)

	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".go" {
			continue
		}

		if err := out.fileUnit(root, dir, e.Name()); err != nil {
			return nil, err
		}

		out.Inputs = append(out.Inputs, filepath.ToSlash(filepath.Join(dir, e.Name())))
	}

	slices.Sort(out.Inputs)
	out.Inputs = slices.Compact(out.Inputs)

	return out, nil
}

func (p *Package) excluded(root, dir, name string) error {
	if !strings.HasSuffix(name, "_test.go") {
		p.whole("build constraints leave out " + name)
	}

	return p.fileUnit(root, dir, name)
}

func (p *Package) fileUnit(root, dir, name string) error {
	b, err := os.ReadFile(filepath.Join(root, dir, name)) //nolint:gosec // a file of the package
	if err != nil {
		return fmt.Errorf("%w %s: %w", ErrRead, dir, err)
	}

	h := newHasher(nil)
	h.str(string(b))

	p.Units = append(p.Units, Unit{
		ID:     goid.New(p.Dir, goid.Excluded, name),
		File:   filepath.ToSlash(filepath.Join(dir, name)),
		Digest: h.sum(),
	})

	return nil
}

func (p *Package) whole(why string) {
	if p.Whole == "" {
		p.Whole = why
	}
}

func shadows(files []*ast.File) map[string]bool {
	out := map[string]bool{}

	for _, f := range files {
		for _, d := range goid.File(".", "x.go", f) {
			_, kind, name, _ := goid.Parse(string(d.ID)) //nolint:errcheck // goid made it
			if kind == goid.Func || kind == goid.Type || kind == goid.Const || kind == goid.Var {
				for n := range strings.SplitSeq(strings.TrimSuffix(strings.TrimSuffix(name, goid.TestSuffix), goid.XTestSuffix), ",") {
					if predeclared[n] {
						out[n] = true
					}
				}
			}
		}
	}

	return out
}

type pending struct {
	unit int
	recv string
	name string
	sig  string
}

func (p *Package) addFile(root string, fset *token.FileSet, f *ast.File, shadowed map[string]bool) {
	path := fset.Position(f.Package).Filename
	rel := filepath.ToSlash(relPath(root, path))
	base := filepath.Base(path)

	if why := wholeReason(f); why != "" && !strings.HasSuffix(base, "_test.go") {
		p.whole(why)
	}

	if !strings.HasSuffix(base, "_test.go") {
		p.name = f.Name.Name
	}

	names := importNames(f)
	for n := range shadowed {
		names[n] = append(names[n], "shadowed")
	}

	for _, d := range goid.File(p.Dir, base, f) {
		h := newHasher(names)
		h.str(string(d.ID.Kind()))

		u := Unit{ID: d.ID, File: rel, Line: fset.Position(d.Pos).Line, EndLine: fset.Position(d.End).Line}

		switch n := d.Node.(type) {
		case *ast.FuncDecl:
			h.directives(n.Doc)
			h.node(n)

			u.Executable = true
			u.Empty = n.Body == nil || len(n.Body.List) == 0
		case *ast.TypeSpec:
			h.directives(d.Gen.Doc, n.Doc)
			h.node(n)
		case *ast.ValueSpec:
			h.directives(d.Gen.Doc, n.Doc)
			h.node(n)

			if d.Gen.Tok == token.CONST {
				constContext(h, d.Gen, n)
			}

			p.Inputs = append(p.Inputs, embedded(h, root, filepath.Dir(path), d.Gen.Doc, n.Doc)...)
		}

		unresolved(h, d.Node, names)
		u.Digest = h.sum()
		p.Units = append(p.Units, u)

		if !strings.HasSuffix(base, "_test.go") {
			p.addAPIPart(d, u.Digest, names)
		}

		if fd, ok := d.Node.(*ast.FuncDecl); ok && d.Recv != "" {
			s := newHasher(names)
			s.str(fd.Name.Name)
			s.node(fd.Recv)
			s.node(fd.Type)
			p.methods = append(p.methods, pending{unit: len(p.Units) - 1, recv: d.Recv, name: fd.Name.Name, sig: s.sum()})
		}
	}
}

// addAPIPart notes what other packages can read of one declaration: a type or
// constant whole, a function's signature and whether its body is absent, empty
// or has statements, a variable's names and type. Whether an initialiser acts
// at start-up is left out: it never changes how an importer's references bind.
func (p *Package) addAPIPart(d goid.Decl, digest string, names map[string][]string) {
	h := newHasher(names)
	h.str(string(d.ID.Kind()))

	switch n := d.Node.(type) {
	case *ast.FuncDecl:
		if d.ID.Kind() == goid.Init {
			return
		}

		sig := *n
		sig.Body = nil

		h.directives(n.Doc)
		h.node(&sig)
		h.str(bodyState(n.Body))
		unresolved(h, &sig, names)
	case *ast.TypeSpec:
		h.str(digest)
	case *ast.ValueSpec:
		if d.Gen.Tok == token.CONST || n.Type == nil {
			if blank(n) {
				return
			}

			h.str(digest)

			break
		}

		if blank(n) {
			return
		}

		h.directives(d.Gen.Doc, n.Doc)

		for _, name := range n.Names {
			h.str(name.Name)
		}

		h.node(n.Type)
		unresolved(h, n.Type, names)
	default:
		return
	}

	if p.api == nil {
		p.api = map[goid.ID]string{}
	}

	p.api[d.ID] = h.sum()
}

func bodyState(b *ast.BlockStmt) string {
	switch {
	case b == nil:
		return "absent"
	case len(b.List) == 0:
		return "empty"
	default:
		return "statements"
	}
}

func blank(n *ast.ValueSpec) bool {
	for _, name := range n.Names {
		if name.Name != "_" {
			return false
		}
	}

	return true
}

// addAPI adds the package's API unit: its name and every declaration's
// part, by ID, so moving a declaration between files keeps it.
func (p *Package) addAPI() {
	if p.name == "" {
		return
	}

	h := newHasher(nil)
	h.str("package " + p.name)

	ids := make([]goid.ID, 0, len(p.api))
	for id := range p.api {
		ids = append(ids, id)
	}

	slices.Sort(ids)

	for _, id := range ids {
		h.str(string(id))
		h.str(p.api[id])
	}

	p.Units = append(p.Units, Unit{ID: goid.New(p.Dir, goid.API, p.name), Digest: h.sum()})
}

// unresolved hashes what a declaration's selectors may name that the
// file's import names don't say: imports whose name can't be guessed, when
// a selector's base matches no import, and dot imports always.
func unresolved(h *hasher, n ast.Node, names map[string][]string) {
	for _, p := range names["."] {
		h.str("R" + p)
	}

	if len(names[""]) == 0 {
		return
	}

	miss := false

	ast.Inspect(n, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if x, ok := sel.X.(*ast.Ident); ok && names[x.Name] == nil {
				miss = true
			}
		}

		return !miss
	})

	if miss {
		for _, p := range names[""] {
			h.str("R" + p)
		}
	}
}

// constContext hashes what a constant spec takes from its group: the values it
// repeats and its iota index, which an inserted constant changes without
// changing the spec's syntax.
func constContext(h *hasher, g *ast.GenDecl, spec *ast.ValueSpec) {
	i := slices.Index(g.Specs, ast.Spec(spec))

	eff := spec

	for j := i; j >= 0; j-- {
		if v, ok := g.Specs[j].(*ast.ValueSpec); ok && v.Values != nil {
			eff = v

			break
		}
	}

	if eff != spec {
		h.str("repeats")
		h.node(eff.Type)

		for _, v := range eff.Values {
			h.node(v)
		}
	}

	usesIota := false

	for _, v := range eff.Values {
		ast.Inspect(v, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == "iota" {
				usesIota = true
			}

			return !usesIota
		})
	}

	if usesIota {
		h.str("iota" + strconv.Itoa(i))
	}
}

func embedded(h *hasher, root, dir string, groups ...*ast.CommentGroup) []string {
	var inputs []string

	for _, g := range groups {
		if g == nil {
			continue
		}

		for _, c := range g.List {
			rest, ok := strings.CutPrefix(c.Text, "//go:embed ")
			if !ok {
				continue
			}

			for pat := range strings.FieldsSeq(rest) {
				pat = strings.TrimPrefix(strings.Trim(pat, "\"`"), "all:")
				// a bad pattern fails the build; it hashes as nothing here
				matches, _ := filepath.Glob(filepath.Join(dir, pat)) //nolint:errcheck // as above

				for _, m := range matches {
					inputs = append(inputs, hashFiles(h, root, dir, m)...)
				}
			}
		}
	}

	return inputs
}

func hashFiles(h *hasher, root, dir, top string) []string {
	var inputs []string

	_ = filepath.WalkDir(top, func(p string, e fs.DirEntry, err error) error { //nolint:errcheck // the callback never fails
		if err != nil || e.IsDir() {
			return nil //nolint:nilerr // an unreadable file hashes as missing
		}

		b, err := os.ReadFile(p) //nolint:gosec // a file the package embeds
		if err != nil {
			return nil //nolint:nilerr // as above
		}

		h.str("F" + filepath.ToSlash(relPath(dir, p)))
		h.str(string(b))

		inputs = append(inputs, filepath.ToSlash(relPath(root, p)))

		return nil
	})

	return inputs
}

// foldMethodSets folds each type's method set into its digest, since a slice
// using a type depends on which methods it has. A type with methods also gets
// a typedecl unit and a methodsig unit per method, for bindings that know
// which methods are reached.
func (p *Package) foldMethodSets() {
	byType := map[goid.ID][]pending{}

	for _, m := range p.methods {
		id := p.Units[m.unit].ID

		typ := goid.New(p.Dir, goid.Type, m.recv)
		if _, suffix, ok := strings.Cut(string(id), "@"); ok && !p.has(typ) {
			typ += goid.ID("@" + suffix)
		}

		byType[typ] = append(byType[typ], m)
	}

	var own []Unit

	for i, u := range p.Units {
		ms := byType[u.ID]
		if len(ms) == 0 {
			continue
		}

		// the type's own declaration, before its method set joins its digest
		_, _, name, _ := goid.Parse(string(u.ID)) //nolint:errcheck // goid made it
		own = append(own, Unit{
			ID: goid.New(p.Dir, goid.TypeDecl, name), File: u.File, Digest: u.Digest,
			Line: u.Line, EndLine: u.EndLine,
		})

		sigs := make([]string, 0, len(ms))

		for _, m := range ms {
			// a test type's methods are bound by the methods tests call
			if u.ID.Test() {
				sigs = append(sigs, m.sig)

				continue
			}

			mu := p.Units[m.unit]
			sig := goid.New(p.Dir, goid.MethodSig, mu.ID.Name())
			own = append(own, Unit{ID: sig, File: mu.File, Digest: m.sig, Line: mu.Line, EndLine: mu.EndLine})

			// an unexported method nothing else in the package names is reached only by
			// calls naming it: reflection and templates see exported methods alone
			if !ast.IsExported(m.name) && !p.shared[m.name] {
				if p.Private == nil {
					p.Private = map[goid.ID][]goid.ID{}
				}

				p.Private[u.ID] = append(p.Private[u.ID], sig)

				continue
			}

			sigs = append(sigs, m.sig)
		}

		slices.Sort(sigs)

		h := newHasher(nil)
		h.str(u.Digest)

		for _, s := range sigs {
			h.str(s)
		}

		p.Units[i].Digest = h.sum()
	}

	p.Units = append(p.Units, own...)
	p.methods = nil
}

// sharedNames are the names an unexported method's presence can matter to
// beyond calls naming it: interface methods, struct fields and method names
// two receiver types declare, since an interface or an embedding can resolve
// to it.
func sharedNames(files []*ast.File) map[string]bool {
	out := map[string]bool{}
	recvs := map[string]map[string]bool{}

	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.InterfaceType:
				addNames(out, n.Methods)
			case *ast.StructType:
				addNames(out, n.Fields)
			case *ast.FuncDecl:
				if n.Recv != nil && len(n.Recv.List) > 0 {
					addRecv(recvs, n.Name.Name, goid.RecvName(n.Recv.List[0].Type))
				}
			}

			return true
		})
	}

	for name, rs := range recvs {
		if len(rs) > 1 {
			out[name] = true
		}
	}

	return out
}

func addNames(out map[string]bool, fl *ast.FieldList) {
	for _, f := range fl.List {
		for _, name := range f.Names {
			out[name.Name] = true
		}

		if len(f.Names) == 0 {
			out[embeddedName(f.Type)] = true
		}
	}
}

func addRecv(recvs map[string]map[string]bool, method, recv string) {
	if recvs[method] == nil {
		recvs[method] = map[string]bool{}
	}

	recvs[method][recv] = true
}

func embeddedName(e ast.Expr) string {
	for {
		switch t := e.(type) {
		case *ast.StarExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.IndexListExpr:
			e = t.X
		case *ast.SelectorExpr:
			return t.Sel.Name
		case *ast.Ident:
			return t.Name
		default:
			return ""
		}
	}
}

func (p *Package) has(id goid.ID) bool {
	return slices.ContainsFunc(p.Units, func(u Unit) bool { return u.ID == id })
}

func wholeReason(f *ast.File) string {
	for _, im := range f.Imports {
		if im.Path.Value == `"C"` {
			return "cgo"
		}
	}

	for _, g := range f.Comments {
		for _, c := range g.List {
			if strings.HasPrefix(c.Text, "//go:linkname") {
				return "go:linkname"
			}
		}
	}

	why := ""

	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncDecl:
			if n.Body == nil {
				why = "a function without a body (" + n.Name.Name + ")"
			}
		case *ast.SelectorExpr:
			switch n.Sel.Name {
			case "MethodByName", "FieldByName", "FieldByNameFunc":
				why = "reflection by name (" + n.Sel.Name + ")"
			}
		}

		return why == ""
	})

	return why
}

func relPath(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}

	return r
}
