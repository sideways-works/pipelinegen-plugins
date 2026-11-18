// Package sqlrefs reads the relations a PostgreSQL statement names with
// PostgreSQL's own parser, compiled to WebAssembly so it needs neither cgo
// nor a database. GORM's `?` and `@name` placeholders are rewritten to `$1`
// first, and fragments are parsed inside a statement that gives them their
// place in the grammar.
package sqlrefs

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"sort"
	"strings"
	"sync"

	pg "github.com/pganalyze/pg_query_go/v6"
	"github.com/sideways-works/pipelinegen-sbom-pg/sqlref"
	pgquery "github.com/wasilibs/go-pgquery"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Ref is a relation a statement names. Its names are as PostgreSQL folds
// them: unquoted lower-cased, quoted as written.
type Ref struct {
	// Schema is empty when the statement doesn't qualify the name.
	Schema string `json:"schema,omitempty"`
	Name   string `json:"name"`
	// Write says the statement changes the relation's rows or definition.
	Write bool `json:"write,omitempty"`
}

// Result is what a parse found: the relations, or why there are none to
// trust.
type Result struct {
	Refs []Ref `json:"refs,omitempty"`
	// Err is the parser's message when the text isn't PostgreSQL.
	Err string `json:"err,omitempty"`
	// Routines are the functions and procedures the statement calls that
	// aren't PostgreSQL's own, by name: their argument types aren't known
	// from the text alone.
	Routines []Ref `json:"routines,omitempty"`
}

type Place int

const (
	// Statement is a whole statement or several (Raw, Exec).
	Statement Place = iota
	// Where is a condition (Where, Or, Not, a finisher's conditions).
	Where
	Having
	// Select is a select list (Select, Distinct).
	Select
	Order
	Group
	// Join is one or more join clauses (Joins with SQL).
	Join
	// From is a FROM item (Table with an expression).
	From
)

// placeholder stands for the relation a fragment's own statement reads, so
// it can be left out of the answer.
const placeholder = "gorm_correlator_fragment_owner"

func wrap(p Place, frag string) string {
	switch p {
	case Where:
		return "SELECT 1 FROM " + placeholder + " WHERE " + frag
	case Having:
		return "SELECT 1 FROM " + placeholder + " GROUP BY 1 HAVING " + frag
	case Select:
		return "SELECT " + frag + " FROM " + placeholder
	case Order:
		return "SELECT 1 FROM " + placeholder + " ORDER BY " + frag
	case Group:
		return "SELECT 1 FROM " + placeholder + " GROUP BY " + frag
	case Join:
		return "SELECT 1 FROM " + placeholder + " " + frag
	case From:
		return "SELECT 1 FROM " + frag
	default:
		return frag
	}
}

// Parser parses statements and remembers what it found, by the text's hash,
// across calls when given a cache file.
type Parser struct {
	mu    sync.Mutex
	seen  map[string]Result
	dirty bool
}

// NewParser makes a parser with what an earlier run found (may be nil).
func NewParser(known map[string]Result) *Parser {
	if known == nil {
		known = map[string]Result{}
	}

	return &Parser{seen: known}
}

// Warm starts compiling the parser in the background: the first parse pays
// for it, a few seconds on a busy machine, which then overlaps with loading
// the Go packages.
func Warm() {
	go func() {
		_, _ = pgquery.Parse("SELECT 1") //nolint:errcheck // only compiles the parser
	}()
}

// Known is what the parser has found so far, for the cache file, and
// whether anything is new since it was made.
func (p *Parser) Known() (map[string]Result, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	out := make(map[string]Result, len(p.seen))
	maps.Copy(out, p.seen)

	return out, p.dirty
}

func (p *Parser) Parse(place Place, sql string) Result {
	text := wrap(place, sql)
	key := hash(text)

	p.mu.Lock()
	r, ok := p.seen[key]
	p.mu.Unlock()

	if ok {
		return r
	}

	r = parse(text)

	p.mu.Lock()
	p.seen[key] = r
	p.dirty = true
	p.mu.Unlock()

	return r
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))

	return hex.EncodeToString(sum[:16])
}

// parse tries the text with GORM's placeholders rewritten, then as written
// (a `?` may be one of PostgreSQL's JSON operators when no values follow).
// A placeholder becomes `($1)` first, which fits wherever GORM puts a
// value, `IN ?` included, where GORM expands a slice to a parenthesised
// list; `$1` alone is tried next.
func parse(text string) Result {
	var (
		tree  *pg.ParseResult
		first error
	)

	for _, t := range []string{placeholders(text, "($1)"), placeholders(text, "$1"), text} {
		var err error

		tree, err = pgquery.Parse(t)
		if err == nil {
			break
		}

		if first == nil {
			first = err
		}
	}

	if tree == nil {
		return Result{Err: first.Error()}
	}

	w := walker{ctes: map[string]bool{}, writes: map[*pg.RangeVar]bool{}, refs: map[Ref]bool{}, funcs: map[Ref]bool{}}
	w.walk(tree.ProtoReflect())

	return w.result()
}

type walker struct {
	ctes   map[string]bool
	writes map[*pg.RangeVar]bool
	refs   map[Ref]bool
	funcs  map[Ref]bool
	// dropped are the names of relations a DROP statement names, which
	// it gives as strings rather than as relations.
	dropped []Ref
}

func (w *walker) walk(m protoreflect.Message) {
	w.visit(m.Interface())

	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsList() && fd.Message() != nil:
			l := v.List()
			for i := range l.Len() {
				w.walk(l.Get(i).Message())
			}
		case fd.IsMap():
		case fd.Message() != nil:
			w.walk(v.Message())
		}

		return true
	})
}

func (w *walker) visit(n any) {
	switch n := n.(type) {
	case *pg.CommonTableExpr:
		w.ctes[n.GetCtename()] = true
	case *pg.InsertStmt:
		w.writes[n.GetRelation()] = true
	case *pg.UpdateStmt:
		w.writes[n.GetRelation()] = true
	case *pg.DeleteStmt:
		w.writes[n.GetRelation()] = true
	case *pg.MergeStmt:
		w.writes[n.GetRelation()] = true
	case *pg.TruncateStmt:
		for _, r := range n.GetRelations() {
			w.writes[r.GetRangeVar()] = true
		}
	case *pg.CopyStmt:
		if n.GetIsFrom() {
			w.writes[n.GetRelation()] = true
		}
	case *pg.CreateStmt:
		w.writes[n.GetRelation()] = true
	case *pg.AlterTableStmt:
		w.writes[n.GetRelation()] = true
	case *pg.IndexStmt:
		w.writes[n.GetRelation()] = true
	case *pg.RenameStmt:
		w.writes[n.GetRelation()] = true
	case *pg.IntoClause:
		w.writes[n.GetRel()] = true
	case *pg.DropStmt:
		w.drop(n)
	case *pg.FuncCall:
		w.call(n.GetFuncname())
	case *pg.CallStmt:
		w.call(n.GetFunccall().GetFuncname())
	case *pg.RangeVar:
		if n.GetRelname() == "" || n.GetRelname() == placeholder || sqlref.System(n.GetSchemaname(), n.GetRelname()) {
			return
		}

		w.refs[Ref{Schema: n.GetSchemaname(), Name: n.GetRelname(), Write: w.writes[n]}] = true
	}
}

func (w *walker) drop(n *pg.DropStmt) {
	switch n.GetRemoveType() {
	case pg.ObjectType_OBJECT_TABLE, pg.ObjectType_OBJECT_VIEW, pg.ObjectType_OBJECT_MATVIEW,
		pg.ObjectType_OBJECT_FOREIGN_TABLE, pg.ObjectType_OBJECT_SEQUENCE:
	default:
		return
	}

	for _, o := range n.GetObjects() {
		var parts []string
		for _, s := range o.GetList().GetItems() {
			parts = append(parts, s.GetString_().GetSval())
		}

		switch len(parts) {
		case 1:
			w.dropped = append(w.dropped, Ref{Name: parts[0], Write: true})
		case 2:
			w.dropped = append(w.dropped, Ref{Schema: parts[0], Name: parts[1], Write: true})
		}
	}
}

// call records a function called by a name that may be the schema's own:
// qualified with a schema other than pg_catalog, or unqualified and not
// one of PostgreSQL's built-ins.
func (w *walker) call(name []*pg.Node) {
	parts := make([]string, 0, len(name))
	for _, p := range name {
		parts = append(parts, p.GetString_().GetSval())
	}

	switch {
	case len(parts) == 1 && !sqlref.Builtin(parts[0]):
		w.funcs[Ref{Name: parts[0]}] = true
	case len(parts) == 2 && parts[0] != "pg_catalog":
		w.funcs[Ref{Schema: parts[0], Name: parts[1]}] = true
	}
}

// result merges what the walk found: a relation both read and written is
// written, and an unqualified name a common table expression defines is
// the query's own, not a relation.
func (w *walker) result() Result {
	write := map[Ref]bool{}

	for r := range w.refs {
		if r.Schema == "" && w.ctes[r.Name] {
			continue
		}

		k := Ref{Schema: r.Schema, Name: r.Name}
		write[k] = write[k] || r.Write
	}

	for _, r := range w.dropped {
		write[Ref{Schema: r.Schema, Name: r.Name}] = true
	}

	var out Result

	for k, wr := range write {
		k.Write = wr
		out.Refs = append(out.Refs, k)
	}

	sortRefs(out.Refs)

	for f := range w.funcs {
		out.Routines = append(out.Routines, f)
	}

	sortRefs(out.Routines)

	return out
}

// Placeholders rewrites GORM's `?` and `@name` placeholders to `$1`,
// leaving strings, quoted names, dollar-quoted bodies and comments alone.
// `@` stays when it is part of an operator (`@>`, `<@`, `@@`).
func Placeholders(s string) string {
	return placeholders(s, "$1")
}

func placeholders(s, param string) string {
	var b strings.Builder

	b.Grow(len(s))

	for i := 0; i < len(s); {
		if n := skip(s, i); n > i {
			b.WriteString(s[i:n])
			i = n

			continue
		}

		c := s[i]

		switch {
		case c == '?':
			b.WriteString(param)

			i++
		case c == '@' && i+1 < len(s) && isIdentStart(s[i+1]) && (i == 0 || !strings.ContainsRune("<@", rune(s[i-1]))):
			j := i + 1
			for j < len(s) && isIdentPart(s[j]) {
				j++
			}

			b.WriteString(param)

			i = j
		default:
			b.WriteByte(c)

			i++
		}
	}

	return b.String()
}

// skip returns the end of a string, quoted name, dollar-quoted body or
// comment starting at i, or i when none starts there.
func skip(s string, i int) int {
	switch {
	case s[i] == '\'' || s[i] == '"':
		q := s[i]

		for j := i + 1; j < len(s); j++ {
			if s[j] == q {
				if j+1 < len(s) && s[j+1] == q {
					j++

					continue
				}

				return j + 1
			}
		}

		return len(s)
	case strings.HasPrefix(s[i:], "--"):
		if j := strings.IndexByte(s[i:], '\n'); j >= 0 {
			return i + j + 1
		}

		return len(s)
	case strings.HasPrefix(s[i:], "/*"):
		if j := strings.Index(s[i+2:], "*/"); j >= 0 {
			return i + 2 + j + 2
		}

		return len(s)
	case s[i] == '$':
		return dollar(s, i)
	}

	return i
}

// dollar returns the end of a dollar-quoted body ($$...$$ or $tag$...$tag$)
// starting at i, or i when `$` starts a parameter instead.
func dollar(s string, i int) int {
	j := i + 1
	for j < len(s) && isIdentPart(s[j]) && (s[j] < '0' || s[j] > '9' || j > i+1) {
		j++
	}

	if j >= len(s) || s[j] != '$' {
		return i
	}

	tag := s[i : j+1]
	if k := strings.Index(s[j+1:], tag); k >= 0 {
		return j + 1 + k + len(tag)
	}

	return len(s)
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

func sortRefs(rs []Ref) {
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].Schema != rs[j].Schema {
			return rs[i].Schema < rs[j].Schema
		}

		return rs[i].Name < rs[j].Name
	})
}
