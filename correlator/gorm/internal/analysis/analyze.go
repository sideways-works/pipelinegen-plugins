package analysis

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/tools/go/packages"

	"github.com/sideways-works/pipelinegen-correlator-gorm/internal/sqlrefs"
	"github.com/sideways-works/pipelinegen-sbom-go/goid"
)

// ErrLoad wraps a failure to load a module's packages.
var ErrLoad = errors.New("loading Go packages")

const loadMode = packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
	packages.NeedImports | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo |
	packages.NeedModule

// Link says that a declaration reads or writes a relation, or that it runs
// a hook or callback GORM calls for it.
type Link struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Kind      string `json:"kind"`
	Narrowing string `json:"narrowing,omitempty"`
	// While is "off" for a wide link that stands only while its narrowing is off.
	While string `json:"while,omitempty"`
}

// Reference tells whether the link comes from a reference in the
// declaration's source, rather than from GORM's machinery or a narrowing's
// wide rule.
func (l Link) Reference() bool {
	return l.Kind != KindCalls && l.Narrowing == ""
}

// The link kinds this correlator emits. reads and writes are the core's;
// calls is a plain link from a declaration to code GORM runs on its behalf.
const (
	KindReads  = "reads"
	KindWrites = "writes"
	KindUses   = "uses"
	KindCalls  = "calls"
)

// CannotTell is a declaration whose relations the code doesn't show.
type CannotTell struct {
	Unit    string   `json:"unit"`
	Reasons []string `json:"reasons"`
}

// Result is what one analysis of a tree found.
type Result struct {
	Modules []string `json:"modules"`
	// Detected says the modules were found, not configured.
	Detected   bool         `json:"detected"`
	Replaces   []string     `json:"replaces,omitempty"`
	Links      []Link       `json:"links"`
	CannotTell []CannotTell `json:"cannot_tell"`
	Models     []ModelInfo  `json:"models"`
	// Evidence holds, per link, the places in the code that made it.
	Evidence map[Link][]string `json:"-"`
	Stats    Stats             `json:"stats"`
}

// Stats counts what the analysis saw, for measuring it on a real tree.
type Stats struct {
	Packages      int `json:"packages"`
	Files         int `json:"files"`
	Decls         int `json:"decls"`
	DeclsWithGORM int `json:"decls_with_gorm"`
	Models        int `json:"models"`
	// ModelsUnknown are models whose table the code doesn't show.
	ModelsUnknown int `json:"models_unknown"`
	TableNames    int `json:"table_name_methods"`
	// Calls are GORM calls that end a statement (Find, Create, Exec...).
	Calls             int            `json:"calls"`
	CallsResolved     int            `json:"calls_resolved"`
	Raw               int            `json:"raw"`
	RawResolved       int            `json:"raw_resolved"`
	ParseFail         int            `json:"parse_failed"`
	Fragments         int            `json:"fragments"`
	FragmentsResolved int            `json:"fragments_resolved"`
	CannotTellDecls   int            `json:"cannot_tell_decls"`
	Reasons           map[string]int `json:"reasons"`
	// Routines counts calls of the schema's own routines in parsed SQL.
	Routines    int           `json:"routine_calls"`
	LoadTime    time.Duration `json:"load_ns"`
	AnalyzeTime time.Duration `json:"analyze_ns"`
}

// Options are the request's settings besides the config.
type Options struct {
	// Parser parses SQL; nil makes one without a cache.
	Parser *sqlrefs.Parser
}

type analyzer struct {
	cfg      Config
	root     string
	modules  []string
	detected bool
	replaces []string
	fset     *token.FileSet
	parser   *sqlrefs.Parser

	funcs     map[string]*funcSrc
	models    map[string]*Model
	namedOf   map[string]*types.Named
	typeDecls map[string]string
	// stores are the handles stored into *gorm.DB fields and package variables,
	// for handles prepared in one place and used in another.
	stores map[string][]stored
	// callbacks are the declarations GORM runs for every statement of a
	// kind (create, query, update, delete, row, raw).
	callbacks map[string][]string
	triggers  map[string]map[string]bool

	links    map[Link]bool
	evidence map[Link][]string
	cannot   map[string]map[string]string
	stats    Stats
	// callers are each loaded function's static call sites, and escapes
	// the functions used other than by calling them.
	callers  map[string][]callSite
	escapes  map[string]bool
	summary  map[string]*chain
	inFlight map[string]bool
}

type funcSrc struct {
	decl *ast.FuncDecl
	info *types.Info
	id   string
}

type stored struct {
	ctx  *declCtx
	expr ast.Expr
}

type fileUnit struct {
	file *ast.File
	pkg  *packages.Package
	ds   []goid.Decl
}

// Analyze loads the configured modules of the tree at root and finds what
// their GORM calls reach.
func Analyze(ctx context.Context, root string, cfg Config, opt Options) (*Result, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLoad, err)
	}

	a := &analyzer{
		cfg: cfg, root: root, fset: token.NewFileSet(),
		funcs: map[string]*funcSrc{}, models: map[string]*Model{}, typeDecls: map[string]string{}, namedOf: map[string]*types.Named{}, stores: map[string][]stored{},
		callbacks: map[string][]string{}, triggers: map[string]map[string]bool{},
		links: map[Link]bool{}, evidence: map[Link][]string{}, cannot: map[string]map[string]string{},
		summary: map[string]*chain{}, callers: map[string][]callSite{}, escapes: map[string]bool{}, inFlight: map[string]bool{}, parser: opt.Parser,
	}
	a.stats.Reasons = map[string]int{}

	if a.parser == nil {
		a.parser = sqlrefs.NewParser(nil)
	}

	a.modules = cfg.Modules
	if len(a.modules) == 0 {
		a.detected = true

		if a.modules, err = DetectModules(root); err != nil {
			return nil, err
		}
	}

	if a.replaces, err = LocalReplaces(root, a.modules); err != nil {
		return nil, err
	}

	start := time.Now()

	units, err := a.load(ctx)
	if err != nil {
		return nil, err
	}

	a.stats.LoadTime = time.Since(start)
	start = time.Now()

	a.prepare(units)

	for _, u := range units {
		for _, d := range u.ds {
			a.declaration(u, d)
		}
	}

	a.linkCallbacks()
	a.linkModels()

	a.stats.AnalyzeTime = time.Since(start)

	return a.result(), nil
}

// load type-checks every package of the configured modules and lists each
// file once (a test variant repeats its package's files).
func (a *analyzer) load(ctx context.Context) ([]fileUnit, error) {
	var units []fileUnit

	seen := map[string]bool{}

	for _, md := range a.modules {
		cfg := &packages.Config{
			Context: ctx, Mode: loadMode, Dir: filepath.Join(a.root, md), Fset: a.fset,
			Tests: a.cfg.tests(), Env: a.cfg.Build.env(), BuildFlags: a.cfg.Build.flags(),
		}

		pkgs, err := packages.Load(cfg, "./...")
		if err != nil {
			if missingModule(err.Error()) {
				return nil, fmt.Errorf("%w in %s: %w", ErrIncomplete, md, err)
			}

			if strings.Contains(err.Error(), "go.mod requires go >=") {
				return nil, fmt.Errorf("%w in %s: %w", ErrToolchain, md, err)
			}

			return nil, fmt.Errorf("%w in %s: %w", ErrLoad, md, err)
		}

		// test variants first: they hold the package's files and its
		// tests, type-checked together
		sort.SliceStable(pkgs, func(i, j int) bool {
			return len(pkgs[i].ID) > len(pkgs[j].ID)
		})

		for _, p := range pkgs {
			if err := loadError(p); err != nil {
				return nil, err
			}

			if p.TypesInfo == nil || strings.HasSuffix(p.ID, ".test") {
				continue
			}

			a.stats.Packages++

			for _, f := range p.Syntax {
				name := a.fset.Position(f.Package).Filename
				if seen[name] || filepath.Ext(name) != ".go" || strings.HasPrefix(relPath(a.root, name), "../") {
					continue
				}

				seen[name] = true
				ds := goid.File(relPath(a.root, filepath.Dir(name)), filepath.Base(name), f)
				units = append(units, fileUnit{file: f, pkg: p, ds: ds})
				a.stats.Files++
				a.stats.Decls += len(ds)
			}
		}
	}

	return units, nil
}

// loadError fails on a package that doesn't parse or type-check. What only
// the go command complains about (a missing embedded file) leaves the types
// whole.
func loadError(p *packages.Package) error {
	// a missing module shows on the package that can't be found, not on its
	// importer
	pkgs := make([]*packages.Package, 0, 1+len(p.Imports))
	pkgs = append(pkgs, p)

	for _, q := range p.Imports {
		pkgs = append(pkgs, q)
	}

	for _, q := range pkgs {
		for _, e := range q.Errors {
			if missingModule(e.Msg) {
				return fmt.Errorf("%w %s: %w", ErrIncomplete, p.ID, e)
			}
		}
	}

	for _, e := range p.Errors {
		if e.Kind == packages.ListError {
			fmt.Fprintf(os.Stderr, "gorm-correlator: %s: %s\n", p.ID, e.Msg)

			continue
		}

		return fmt.Errorf("%w %s: %w", ErrLoad, p.ID, e)
	}

	return nil
}

// ErrIncomplete wraps a load that failed for a dependency missing from the
// module cache: the answer can't be known from the tree alone.
var ErrIncomplete = errors.New("a dependency is missing from the module cache")

// ErrToolchain wraps a load that failed because the Go toolchain on the path
// is older than the module requires.
var ErrToolchain = errors.New("the Go toolchain is older than the module requires")

func missingModule(msg string) bool {
	for _, s := range []string{"cannot find module providing package", "missing go.sum entry", "module lookup disabled", "no required module provides package"} {
		if strings.Contains(msg, s) {
			return true
		}
	}

	return false
}

func (a *analyzer) prepare(units []fileUnit) {
	for _, u := range units {
		for _, d := range u.ds {
			if ts, ok := d.Node.(*ast.TypeSpec); ok && u.pkg.Types != nil {
				a.typeDecls[u.pkg.Types.Path()+"."+ts.Name.Name] = string(d.ID)
			}

			if fd, ok := d.Node.(*ast.FuncDecl); ok {
				if fn, ok := u.pkg.TypesInfo.Defs[fd.Name].(*types.Func); ok {
					a.funcs[funcKey(fn)] = &funcSrc{decl: fd, info: u.pkg.TypesInfo, id: string(d.ID)}
				}
			}
		}
	}

	for _, u := range units {
		for _, d := range u.ds {
			c := a.newCtx(u.pkg, d)
			c.findStores()
			c.findCallbacks()
			c.findCalls()
		}
	}
}

func (a *analyzer) declaration(u fileUnit, d goid.Decl) {
	c := a.newCtx(u.pkg, d)
	c.run()

	if c.gorm {
		a.stats.DeclsWithGORM++
	}
}

func (a *analyzer) newCtx(p *packages.Package, d goid.Decl) *declCtx {
	return &declCtx{
		a: a, d: d, id: string(d.ID), info: p.TypesInfo, pkg: p.Types,
		chains: map[ast.Expr]*chain{}, busy: map[types.Object]bool{},
	}
}

func (a *analyzer) add(l Link, where string) {
	a.links[l] = true
	if len(a.evidence[l]) < 8 {
		a.evidence[l] = append(a.evidence[l], where)
	}
}

func (a *analyzer) cantTell(unit, reason, where string) {
	if a.cannot[unit] == nil {
		a.cannot[unit] = map[string]string{}
	}

	if _, ok := a.cannot[unit][reason]; !ok {
		a.cannot[unit][reason] = where
		a.stats.Reasons[reason]++
	}
}

func (a *analyzer) pos(p token.Pos) string {
	ps := a.fset.Position(p)

	return fmt.Sprintf("%s:%d", relPath(a.root, ps.Filename), ps.Line)
}

func relPath(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return filepath.ToSlash(p)
	}

	return filepath.ToSlash(r)
}

func (a *analyzer) result() *Result {
	r := &Result{Modules: a.modules, Detected: a.detected, Replaces: a.replaces, Evidence: a.evidence, Stats: a.stats}

	for l := range a.links {
		r.Links = append(r.Links, l)
	}

	sort.Slice(r.Links, func(i, j int) bool {
		x, y := r.Links[i], r.Links[j]
		if x.From != y.From {
			return x.From < y.From
		}

		if x.To != y.To {
			return x.To < y.To
		}

		if x.Kind != y.Kind {
			return x.Kind < y.Kind
		}

		return x.Narrowing+x.While < y.Narrowing+y.While
	})

	for u, rs := range a.cannot {
		ct := CannotTell{Unit: u}

		for reason, where := range rs {
			if where != "" {
				reason += " at " + where
			}

			ct.Reasons = append(ct.Reasons, reason)
		}

		sort.Strings(ct.Reasons)
		r.CannotTell = append(r.CannotTell, ct)
	}

	sort.Slice(r.CannotTell, func(i, j int) bool { return r.CannotTell[i].Unit < r.CannotTell[j].Unit })
	r.Stats.CannotTellDecls = len(r.CannotTell)

	for _, m := range a.models {
		r.Models = append(r.Models, m.info())
	}

	sort.Slice(r.Models, func(i, j int) bool { return r.Models[i].Type < r.Models[j].Type })
	r.Stats.Models = len(r.Models)

	for _, m := range r.Models {
		if m.Unknown != "" {
			r.Stats.ModelsUnknown++
		}
	}

	return r
}

// trace tells people tuning the config what the parser refused, when
// GORM_CORRELATOR_TRACE is set.
func (a *analyzer) trace(p token.Pos, msg, sql string) {
	if os.Getenv("GORM_CORRELATOR_TRACE") == "" {
		return
	}

	if len(sql) > 300 {
		sql = sql[:300] + "..."
	}

	fmt.Fprintf(os.Stderr, "%s: %s: %s\n", a.pos(p), msg, sql)
}
