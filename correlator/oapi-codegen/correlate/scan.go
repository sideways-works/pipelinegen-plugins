package correlate

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/sideways-works/pipelinegen-sbom-go/goid"
)

// accessors are the generated functions that hand out the embedded document.
var accessors = []string{"GetSpec", "GetSwagger", "GetSpecJSON", "PathToRawSpec"}

var errNoModule = errors.New("no go.mod")

// HandlerLinks links the repository's methods named after an operation that
// take its RequestObject to that operation, so a test that calls a handler
// directly runs when the operation changes in ways no generated type shows.
func (m *Model) HandlerLinks(tree string) ([]Link, error) {
	cache := map[string][]parsed{}

	var out []Link

	for _, d := range m.Config.Documents {
		if d.Go == nil {
			continue
		}

		dirs := d.Go.Handlers
		if len(dirs) == 0 {
			dirs = m.Config.Scan
		}

		key := strings.Join(dirs, "\x00")

		files, ok := cache[key]
		if !ok {
			var err error

			files, err = m.parse(tree, dirs, false)
			if err != nil {
				return nil, err
			}

			cache[key] = files
		}

		gen, err := m.importPath(d.Go.Package)
		if err != nil {
			return nil, err
		}

		byRequest := map[string]string{}
		for _, op := range m.OperationsOf(d.Path) {
			byRequest[op.GoName+"RequestObject"] = op.ID
		}

		for _, f := range files {
			out = append(out, handlersIn(f, gen, byRequest)...)
		}
	}

	slices.SortFunc(out, func(a, b Link) int { return strings.Compare(a.From+" "+a.To, b.From+" "+b.To) })

	return slices.CompactFunc(out, func(a, b Link) bool { return a.From == b.From && a.To == b.To }), nil
}

func handlersIn(f parsed, gen string, byRequest map[string]string) []Link {
	local := ""

	for _, imp := range f.file.Imports {
		if p, err := strconv.Unquote(imp.Path.Value); err == nil && p == gen {
			local = path.Base(p)
			if imp.Name != nil {
				local = imp.Name.Name
			}
		}
	}

	if local == "" {
		return nil
	}

	var out []Link

	for _, decl := range f.file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
			continue
		}

		for _, p := range fn.Type.Params.List {
			sel, ok := p.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != fn.Name.Name+"RequestObject" {
				continue
			}

			if x, ok := sel.X.(*ast.Ident); !ok || x.Name != local {
				continue
			}

			if id, ok := byRequest[sel.Sel.Name]; ok {
				out = append(out, Link{
					From: GoUnit(f.dir, "method", recvName(fn.Recv.List[0].Type)+"."+fn.Name.Name),
					To:   id, Kind: LinkImplements,
					Note: "a handler method that takes the operation's request",
				})
			}
		}
	}

	return out
}

func recvName(t ast.Expr) string {
	switch x := t.(type) {
	case *ast.StarExpr:
		return recvName(x.X)
	case *ast.IndexExpr:
		return recvName(x.X)
	case *ast.IndexListExpr:
		return recvName(x.X)
	case *ast.Ident:
		return x.Name
	default:
		return ""
	}
}

// A Referrer is a top-level declaration that reaches the embedded document
// through an accessor, directly or through a variable that only wraps one.
type Referrer struct {
	ID       string `json:"id"`
	Document string `json:"document"`
	// Through names the accessor or wrapper it uses.
	Through string `json:"through"`
	// As is "reader" or "per-operation" when the config classifies it,
	// "wrapper" for a variable that only hands an accessor on (its users
	// are listed instead), and empty when nobody has said.
	As string `json:"as"`
}

type name struct{ pkg, ident string }

type parsed struct {
	dir        string
	name       string
	importPath string
	file       *ast.File
}

// Referrers finds the declarations in the scanned directories that reach a
// document's embedded copy. A variable that only passes an accessor on
// (sync.OnceValues(GetSpec)) becomes an accessor itself; everything else is a
// referrer the config must classify.
func (m *Model) Referrers(tree string) ([]Referrer, error) {
	files, err := m.parse(tree, m.Config.Scan, false)
	if err != nil {
		return nil, err
	}

	var out []Referrer

	for _, d := range m.Config.Documents {
		if d.Go == nil {
			continue
		}

		gen, err := m.importPath(d.Go.Package)
		if err != nil {
			return nil, err
		}

		out = append(out, referrersOf(files, d, gen)...)
	}

	return out, nil
}

func (m *Model) parse(tree string, dirs []string, tests bool) ([]parsed, error) {
	var out []parsed

	fset := token.NewFileSet()

	for _, rel := range m.goFiles {
		if (!tests && strings.HasSuffix(rel, "_test.go")) || !under(dirs, rel) {
			continue
		}

		f, ok := m.parsed[rel]
		if !ok {
			var err error

			f, err = parser.ParseFile(fset, filepath.Join(tree, filepath.FromSlash(rel)), nil, parser.SkipObjectResolution)
			if err != nil {
				return nil, fmt.Errorf("parse: %w", err)
			}

			m.parsed[rel] = f
		}

		d := path.Dir(rel)
		ip, _ := m.importPath(d) //nolint:errcheck // a directory outside every module imports nothing by path

		out = append(out, parsed{dir: d, name: path.Base(rel), importPath: ip, file: f})
	}

	return out, nil
}

func referrersOf(files []parsed, d Document, gen string) []Referrer {
	known := map[name]string{}
	for _, a := range accessors {
		known[name{gen, a}] = a
	}

	wrappers := findWrappers(files, d, known)

	return collectReferrers(files, d, known, wrappers)
}

func findWrappers(files []parsed, d Document, known map[name]string) map[string]Referrer {
	wrappers := map[string]Referrer{}

	for {
		grew := false

		for _, f := range files {
			for _, decl := range goid.File(f.dir, f.name, f.file) {
				vs, ok := decl.Node.(*ast.ValueSpec)
				if !ok || decl.Gen.Tok != token.VAR || len(vs.Values) == 0 {
					continue
				}

				uses := usesOf(vs, f, known)
				if len(uses) == 0 || anyCalled(uses) {
					continue
				}

				for _, n := range vs.Names {
					key := name{f.importPath, n.Name}
					if _, ok := known[key]; !ok {
						known[key] = n.Name
						wrappers[string(decl.ID)] = Referrer{ID: string(decl.ID), Document: d.Path, Through: uses[0].through, As: "wrapper"}
						grew = true
					}
				}
			}
		}

		if !grew {
			return wrappers
		}
	}
}

func collectReferrers(files []parsed, d Document, known map[name]string, wrappers map[string]Referrer) []Referrer {
	var out []Referrer

	for _, f := range files {
		if path.Clean(f.dir) == path.Clean(d.Go.Package) {
			continue
		}

		for _, decl := range goid.File(f.dir, f.name, f.file) {
			id := string(decl.ID)

			if w, ok := wrappers[id]; ok {
				out = append(out, w)

				continue
			}

			if uses := usesOf(decl.Node, f, known); len(uses) > 0 {
				out = append(out, Referrer{ID: id, Document: d.Path, Through: uses[0].through, As: classify(d.Go, id)})
			}
		}
	}

	slices.SortFunc(out, func(a, b Referrer) int { return strings.Compare(a.ID, b.ID) })

	return slices.CompactFunc(out, func(a, b Referrer) bool { return a.ID == b.ID })
}

type use struct {
	through string
	called  bool
}

func anyCalled(uses []use) bool {
	return slices.ContainsFunc(uses, func(u use) bool { return u.called })
}

// usesOf finds the references in n to known names: pkg.Name through an
// import, or a bare Name within the same package. Shadowing a name locally
// is rare enough to count as a use; that only lists more.
func usesOf(n ast.Node, f parsed, known map[name]string) []use {
	locals := map[string]string{}

	for _, imp := range f.file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}

		local := path.Base(p)
		if imp.Name != nil {
			local = imp.Name.Name
		}

		locals[local] = p
	}

	match := func(e ast.Expr) (string, bool) {
		switch x := e.(type) {
		case *ast.SelectorExpr:
			id, ok := x.X.(*ast.Ident)
			if !ok {
				return "", false
			}

			p, ok := locals[id.Name]
			if !ok {
				return "", false
			}

			_, ok = known[name{p, x.Sel.Name}]

			return id.Name + "." + x.Sel.Name, ok
		case *ast.Ident:
			_, ok := known[name{f.importPath, x.Name}]

			return x.Name, ok && f.importPath != ""
		default:
			return "", false
		}
	}

	called := map[ast.Expr]bool{}

	ast.Inspect(n, func(x ast.Node) bool {
		if c, ok := x.(*ast.CallExpr); ok {
			called[c.Fun] = true
		}

		return true
	})

	var out []use

	ast.Inspect(n, func(x ast.Node) bool {
		e, ok := x.(ast.Expr)
		if !ok {
			return true
		}

		if through, ok := match(e); ok {
			out = append(out, use{through: through, called: called[e]})

			return false
		}

		return true
	})

	return out
}

func classify(g *GoConfig, id string) string {
	switch {
	case slices.Contains(g.Readers, id):
		return "reader"
	case slices.Contains(g.PerOperation, id):
		return "per-operation"
	default:
		return ""
	}
}

func (m *Model) importPath(dir string) (string, error) {
	dir = path.Clean(dir)

	for cur := dir; ; cur = path.Dir(cur) {
		if mod, ok := m.modules[cur]; ok {
			if cur == "." {
				if dir == "." {
					return mod, nil
				}

				return mod + "/" + dir, nil
			}

			if rel := strings.TrimPrefix(strings.TrimPrefix(dir, cur), "/"); rel != "" {
				return mod + "/" + rel, nil
			}

			return mod, nil
		}

		if cur == "." {
			return "", fmt.Errorf("%w above %s", errNoModule, dir)
		}
	}
}

func modulePath(data []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "module"); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}

	return ""
}
