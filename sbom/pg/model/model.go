// Package model builds a database's objects from its migration files alone.
// A unit's digest covers the parse trees of the statements that shaped it, in
// order; what can't be tied to a unit (dynamic SQL, an unknown statement)
// goes into every digest.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"sort"
	"strings"

	"github.com/sideways-works/pipelinegen-sbom-pg/pgid"
	"github.com/sideways-works/pipelinegen-sbom-pg/sqlref"
)

type Unit struct {
	ID   string
	Kind string
	// Digest is empty for an alias, whose digest is DigestFrom's.
	Digest     string
	DigestFrom []string
	// File is the migration that created the object.
	File string
}

// Link says that when From is bound, To is bound too.
type Link struct {
	From, To, Kind string
}

// The schema new objects go to by default, and PostgreSQL's own.
const (
	defaultSchema = "public"
	catalogSchema = "pg_catalog"
)

const (
	Reads  = "reads"
	Writes = "writes"
	Uses   = "uses"
	// Changes links a migration file to the units its statements changed.
	Changes = "changes"
)

// object is one database object while the migrations are applied. Its id
// is computed at the end: a rename changes the name, never the object.
type object struct {
	kind, schema, name string
	// args are a function's argument types, as PostgreSQL prints them
	args    []string
	created string
	history []string
	// data is the statements that wrote the table's rows
	data []string
	// constraints maps a table's constraint names to the table a foreign
	// key points at (nil for other constraints)
	constraints map[string]*object
	triggers    map[string][]*object
	indexes     map[string]bool
	// refs are the names a definition reads or writes, resolved against the
	// final schema
	refs []pendingRef
	uses map[*object]string
	// dataFrom are the relations a data statement read while writing
	dataFrom []pendingRef
	// owned are the sequences a table's serial and identity columns made,
	// or that are OWNED BY one of its columns: they go with the table
	owned []*object
	// extSchema is the schema an extension put its objects in
	extSchema string
	dynamic   bool
	dropped   bool
}

type pendingRef struct {
	ref  sqlref.Ref
	path []string
	kind string
}

func (o *object) id() string {
	switch o.kind {
	case pgid.Extension:
		return pgid.Ext(o.name)
	case pgid.Function:
		return pgid.Func(o.schema, o.name, o.args)
	default:
		return pgid.Of(o.kind, o.schema, o.name)
	}
}

func (o *object) use(to *object) {
	if to == nil || to == o {
		return
	}

	if o.uses == nil {
		o.uses = map[*object]string{}
	}

	o.uses[to] = Uses
}

type Report struct {
	Files      int            `json:"files"`
	Statements int            `json:"statements"`
	Kinds      map[string]int `json:"kinds"`
	// Opaque lists statements no unit could be tied to, by file; they
	// are in every unit's digest.
	Opaque []string `json:"opaque"`
	// Unresolved lists statements naming an object the model doesn't know,
	// folded into the units around them.
	Unresolved []string `json:"unresolved"`
	// Dynamic lists functions that build SQL at run time: their links
	// are incomplete.
	Dynamic []string `json:"dynamic"`
	// Unparsed lists files PostgreSQL's parser refused; they are opaque.
	Unparsed []string `json:"unparsed"`
}

type Model struct {
	// path is the search_path in force: SET search_path lasts for the
	// session the migrations run in, SET LOCAL until the file's end
	path, sessionPath []string
	objects           []*object
	file              string
	changed           map[string]map[*object]bool
	// stepIDs holds, per file, the ids its statements changed as they were
	// at the time: a table renamed or dropped shows under its old name too
	stepIDs map[string]map[string]bool
	files   []string
	opaque  []string
	report  Report
}

// New starts an empty database whose search_path is path (default public).
func New(path []string) *Model {
	if len(path) == 0 {
		path = []string{defaultSchema}
	}

	return &Model{
		path:        path,
		sessionPath: path,
		changed:     map[string]map[*object]bool{},
		stepIDs:     map[string]map[string]bool{},
		report:      Report{Kinds: map[string]int{}},
	}
}

func (m *Model) touch(o *object, fp string) {
	if o == nil {
		return
	}

	o.history = append(o.history, fp)
	m.mark(o)
}

func (m *Model) mark(o *object) {
	if m.changed[m.file] == nil {
		m.changed[m.file] = map[*object]bool{}
	}

	m.changed[m.file][o] = true

	if m.stepIDs[m.file] == nil {
		m.stepIDs[m.file] = map[string]bool{}
	}

	m.stepIDs[m.file][o.id()] = true
}

func (m *Model) add(kind, schema, name string, fp string) *object {
	o := &object{kind: kind, schema: schema, name: name, created: m.file}

	// an object in pg_temp lives for the migration's session only
	if schema == "pg_temp" {
		o.dropped = true

		return o
	}

	m.objects = append(m.objects, o)
	m.touch(o, fp)

	return o
}

// find looks an object up by kind and name; an unqualified name is looked
// up along the search path.
func (m *Model) find(kinds []string, schema, name string) *object {
	schemas := []string{schema}
	if schema == "" {
		schemas = m.path
	}

	for _, s := range schemas {
		for _, o := range m.objects {
			if o.dropped || o.schema != s || o.name != name {
				continue
			}

			if slices.Contains(kinds, o.kind) {
				return o
			}
		}
	}

	return nil
}

// functions finds the overloads of a function; args, when not nil, picks
// one.
func (m *Model) functions(schema, name string, args []string) []*object {
	schemas := []string{schema}
	if schema == "" {
		schemas = m.path
	}

	for _, s := range schemas {
		var out []*object

		for _, o := range m.objects {
			if o.dropped || o.kind != pgid.Function || o.schema != s || o.name != name {
				continue
			}

			if args != nil && strings.Join(o.args, ",") != strings.Join(args, ",") {
				continue
			}

			out = append(out, o)
		}

		if len(out) > 0 {
			return out
		}
	}

	return nil
}

func (m *Model) creationSchema(schema string) string {
	if schema != "" {
		return schema
	}

	for _, s := range m.path {
		if s != "$user" && s != catalogSchema {
			return s
		}
	}

	return defaultSchema
}

func (m *Model) extension(name string) *object {
	for _, o := range m.objects {
		if !o.dropped && o.kind == pgid.Extension && o.name == name {
			return o
		}
	}

	return nil
}

var relationKinds = []string{pgid.Table, pgid.View, pgid.Sequence}

func (m *Model) relation(schema, name string) *object {
	return m.find(relationKinds, schema, name)
}

func (m *Model) indexOwner(schema, name string) *object {
	schemas := []string{schema}
	if schema == "" {
		schemas = m.path
	}

	for _, s := range schemas {
		for _, o := range m.objects {
			if !o.dropped && o.indexes[s+"."+name] {
				return o
			}
		}
	}

	return nil
}

type Result struct {
	Units []Unit
	Links []Link
	// Wide are the links that stand only while RowsToReaders is off.
	Wide   []Link
	Report Report
}

// RowsToReaders is the narrowing by which a slice that only wrote a table
// doesn't bind the rows the migrations left in it. It can miss an insert
// that starts conflicting with a row a new migration adds.
const RowsToReaders = "rows-to-readers"

func (m *Model) Result() Result {
	opaque := ""
	if len(m.opaque) > 0 {
		opaque = "opaque " + strings.Join(m.opaque, "\n")
	}

	r := &results{seen: map[string]bool{}, overloads: map[string][]string{}}

	for _, o := range m.objects {
		if !o.dropped {
			m.objectResult(r, o, opaque)
		}
	}

	m.extensionRoutines(r)

	for name, ids := range r.overloads {
		sort.Strings(ids)
		r.unit(Unit{ID: name, Kind: pgid.RoutineKind, DigestFrom: ids})

		for _, id := range ids {
			r.link(name, id, Uses)
		}
	}

	for _, f := range m.files {
		for id := range m.stepIDs[f] {
			if r.seen[id] {
				r.link("file:"+f, id, Changes)
			}
		}
	}

	sort.Slice(r.units, func(i, j int) bool { return r.units[i].ID < r.units[j].ID })

	rep := m.report
	rep.Opaque = append([]string(nil), m.report.Opaque...)
	rep.Dynamic = r.dynamic
	sort.Strings(rep.Dynamic)

	return Result{Units: r.units, Links: dedupe(r.links), Wide: dedupe(r.narrowed), Report: rep}
}

// extensionRoutines adds pg:routine: aliases for the functions installed
// extensions provide, so that a call of st_x() binds PostGIS.
func (m *Model) extensionRoutines(r *results) {
	byName := map[string][]string{}

	for _, o := range m.objects {
		if o.dropped || o.kind != pgid.Extension {
			continue
		}

		for fn, exts := range provided["function"] {
			if sqlref.Builtin(fn) || !slices.Contains(exts, o.name) {
				continue
			}

			name := pgid.Routine(o.extSchema, fn)
			if _, own := r.overloads[name]; own {
				continue
			}

			byName[name] = append(byName[name], o.id())
		}
	}

	for name, ids := range byName {
		sort.Strings(ids)
		r.unit(Unit{ID: name, Kind: pgid.RoutineKind, DigestFrom: ids})

		for _, id := range ids {
			r.link(name, id, Uses)
		}
	}
}

type results struct {
	units     []Unit
	links     []Link
	narrowed  []Link
	seen      map[string]bool
	overloads map[string][]string
	dynamic   []string
}

func (r *results) unit(u Unit) {
	if !r.seen[u.ID] {
		r.seen[u.ID] = true
		r.units = append(r.units, u)
	}
}

func (r *results) link(from, to, kind string) {
	r.links = append(r.links, Link{From: from, To: to, Kind: kind})
}

func (m *Model) objectResult(r *results, o *object, opaque string) {
	id := o.id()

	history := strings.Join(o.history, "\n")
	if opaque != "" && o.kind != pgid.Extension {
		history += "\n" + opaque
	}

	r.unit(Unit{ID: id, Kind: o.kind, Digest: digest(history), File: o.created})

	switch o.kind {
	case pgid.Table, pgid.View:
		alias := pgid.Of(pgid.Relation, o.schema, o.name)
		r.unit(Unit{ID: alias, Kind: pgid.Relation, DigestFrom: []string{id}, File: o.created})
		r.link(alias, id, Uses)
	case pgid.Function:
		name := pgid.Routine(o.schema, o.name)
		r.overloads[name] = append(r.overloads[name], id)
	}

	if len(o.data) > 0 {
		did := pgid.Of(pgid.Data, o.schema, o.name)
		r.unit(Unit{ID: did, Kind: pgid.Data, Digest: digest(strings.Join(o.data, "\n")), File: o.created})

		// rows copied from a table whose rows no migration wrote come from
		// nothing a unit describes: the copy depends on its definition,
		// which the statement's history holds
		for _, ref := range o.dataFrom {
			for _, to := range m.resolve(ref) {
				if len(to.data) > 0 {
					r.link(did, pgid.Of(pgid.Data, to.schema, to.name), Reads)
				}
			}
		}
	}

	for to, kind := range o.uses {
		if !to.dropped {
			r.link(id, to.id(), kind)
		}
	}

	for _, ref := range o.refs {
		for _, to := range m.resolve(ref) {
			r.link(id, to.id(), ref.kind)

			// what reads a table through a view or a function reads the
			// rows the migrations left in it too
			if ref.kind == Reads && len(to.data) > 0 {
				r.link(id, pgid.Of(pgid.Data, to.schema, to.name), Reads)
			}
		}
	}

	if (o.kind == pgid.Table || o.kind == pgid.View) && len(o.data) > 0 {
		// writers bind the rows as well: an insert can conflict with a
		// row a migration or a seed wrote
		r.narrowed = append(r.narrowed, Link{
			From: pgid.Of(pgid.Relation, o.schema, o.name), To: pgid.Of(pgid.Data, o.schema, o.name), Kind: Uses,
		})
	}

	if o.dynamic {
		r.dynamic = append(r.dynamic, id)
	}
}

func (m *Model) resolve(p pendingRef) []*object {
	saved := m.path
	m.path = p.path

	defer func() { m.path = saved }()

	if p.ref.Function {
		return m.functions(p.ref.Schema, p.ref.Name, nil)
	}

	if o := m.find([]string{pgid.Table, pgid.View}, p.ref.Schema, p.ref.Name); o != nil {
		return []*object{o}
	}

	return nil
}

// Changed lists, per migration file, the ids of the units its
// statements changed, as they were named when the file ran: a table the
// file renamed shows under both names, one it dropped under its last.
func (m *Model) Changed() map[string][]string {
	out := map[string][]string{}

	for _, f := range m.files {
		ids := make([]string, 0, len(m.stepIDs[f]))
		for id := range m.stepIDs[f] {
			ids = append(ids, id)
		}

		sort.Strings(ids)
		out[f] = ids
	}

	return out
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))

	return hex.EncodeToString(sum[:])
}

func dedupe(links []Link) []Link {
	seen := map[Link]bool{}
	out := links[:0]

	for _, l := range links {
		if l.From == l.To || seen[l] {
			continue
		}

		seen[l] = true

		out = append(out, l)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}

		if out[i].To != out[j].To {
			return out[i].To < out[j].To
		}

		return out[i].Kind < out[j].Kind
	})

	return out
}
