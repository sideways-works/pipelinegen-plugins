package openapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Namespace is the prefix of every unit id this package makes.
const Namespace = "api"

// Unit kinds besides the components' own (schema, parameter, response and
// the other sections of components).
const (
	KindOperation = "operation"
	// KindDocument is a whole file: what a test that reads the file
	// depends on.
	KindDocument = "document"
	// KindTop is a part of a document every operation shares: its servers,
	// its version and dialect.
	KindTop = "top"
	// KindFragment is a top-level entry of a file that is not a document
	// itself, only the target of refs from one.
	KindFragment = "fragment"
)

// LinkUses is the kind of every link between units: one refers to the
// other.
const LinkUses = "uses"

// ErrInvalid marks a document that isn't well formed.
var ErrInvalid = errors.New("invalid document")

// ErrCannotTell marks a document whose units can't be read from the commit
// (a remote ref, a missing file, a cycle of path items).
var ErrCannotTell = errors.New("cannot tell from the tree")

// A Unit is something a change can touch and a test can depend on.
type Unit struct {
	ID     string `json:"id"`
	Group  string `json:"group"`
	Digest string `json:"digest"`
	File   string `json:"file"`
	Kind   string `json:"kind"`
	// Executable says whether an observation can ever report the unit as
	// touched: an operation can (a request reached it), a schema can't.
	Executable bool `json:"executable"`
	// Label is the unit as people name it ("GET /notes/{id} (getNote)"),
	// for pipelinegen's why only: nothing hashes, caches or compares it.
	Label string `json:"label,omitempty"`
}

// A Link says that a test bound to From is bound to To too.
type Link struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Kind      string `json:"kind"`
	Narrowing string `json:"narrowing,omitempty"`
	// While says when a link of a narrowing holds: "off" for a wide link
	// that stands while the narrowing is off, "on" for a narrow one.
	While string `json:"while,omitempty"`
}

// Narrowings this package's units and links belong to.
const (
	// NarrowDocText leaves documentation out of the units' digests; while
	// it is off, each unit links to its documentation unit.
	NarrowDocText = "doc-text"
	// NarrowDocument binds a test that depends on a contract file to the
	// units of it the test touched, instead of the whole file.
	NarrowDocument = "document-units"
)

// LinkChanges says what a file defines: a link from file:<path> to each of
// its units.
const LinkChanges = "changes"

// KindDocs is a unit's documentation: its descriptions, summaries, titles
// and examples, bound only while the doc-text narrowing is off.
const KindDocs = "docs"

// DocsID is the id of a unit's documentation unit.
func DocsID(id string) string {
	return Namespace + ":docs:" + strings.TrimPrefix(id, Namespace+":")
}

// An Operation is one method on one path of a document.
type Operation struct {
	ID          string // the unit id, api:<prefix><operationId>
	OperationID string // as written, or derived from the method and path
	Method      string // upper case
	Path        string
	File        string
	// From is the file the path item was written in: File, or the file
	// a $ref in File's paths led to.
	From string
	// GoName is the name oapi-codegen gives the operation's methods and types.
	GoName string
}

// A Document is one contract the repository serves.
type Document struct {
	Path string `json:"path"`
	// Prefix goes before every operationId of this document in unit ids,
	// for repositories whose contracts reuse an operationId.
	Prefix string `json:"prefix,omitempty"`
}

// Options are the parts of the config that change digests.
type Options struct {
	// KeepDocs keeps documentation in the digests, for tests that assert on it.
	KeepDocs bool `json:"keep_docs,omitempty"`
}

// Model is what a tree's documents hold.
type Model struct {
	Units      []Unit
	Links      []Link
	Operations []Operation
	// Servers lists, per document, its servers' URLs as written.
	Servers map[string][]string
	files   []string
	reads   map[string][]string
	byDoc   map[string][]string
	top     map[string][]string
}

type node struct {
	unit     Unit
	own      string
	edges    []string
	external []string
	// text is the digest of the node as written, documentation included.
	text string
}

// builder makes the units of one file. A ref into another file becomes a
// link to that file's unit, never a read of it, so a file of parts is cached
// by its own content.
type builder struct {
	file  string
	doc   bool
	l     *loader
	opts  Options
	nodes map[string]*node
	order []string
	errs  []error
}

// Build reads each document and returns its units and the links between
// them. A file a document read along is read as parts too, so every link into
// it names a unit.
func Build(tree string, docs []Document, opts Options) (*Model, error) {
	m := &Model{Servers: map[string][]string{}, reads: map[string][]string{}, byDoc: map[string][]string{}, top: map[string][]string{}}
	seen := map[string]Operation{}
	queue := slices.Clone(docs)
	listed := map[string]bool{}

	for _, d := range docs {
		listed[d.Path] = true
	}

	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]

		b := &builder{file: d.Path, l: newLoader(tree), opts: opts, nodes: map[string]*node{}}

		ops, err := b.document(d, m)
		if err != nil {
			return nil, err
		}

		m.files = append(m.files, d.Path)
		m.reads[d.Path] = b.l.files()

		for _, f := range m.reads[d.Path] {
			if !listed[f] && !isDocument(asMap(b.l.docs[f])) {
				listed[f] = true
				queue = append(queue, Document{Path: f})
			}
		}

		for _, op := range ops {
			if other, dup := seen[op.ID]; dup {
				return nil, duplicate(other, op)
			}

			seen[op.ID] = op
		}

		m.Operations = append(m.Operations, ops...)
	}

	m.linkRoutes()
	m.label()

	return m, nil
}

// duplicate explains two operations with one id: which document serves it
// can't be read from the files, and a prefix on one document settles it.
func duplicate(a, b Operation) error {
	where := func(op Operation) string {
		if op.From != op.File {
			return op.File + " (its path " + op.Path + " is a $ref into " + op.From + ")"
		}

		return op.File
	}

	err := ErrInvalid
	if a.From == b.File || b.From == a.File {
		err = ErrCannotTell
	}

	return fmt.Errorf("%w: operation id %s is in %s and %s: give one document a prefix", err, b.OperationID, where(a), where(b))
}

// ReadFiles lists the files the units of file were read from; its answer is
// cached by all of them.
func (m *Model) ReadFiles(file string) []string { return m.reads[file] }

// routes adds the unit of a document's routing table. A router picks the
// most specific pattern, so adding, removing or moving any route can move
// requests between operations; every operation depends on this unit.
func (b *builder) routes(ops []Operation) string {
	var table []string

	for _, op := range ops {
		if !strings.HasPrefix(op.Path, webhookPrefix) {
			table = append(table, op.Method+" "+op.Path)
		}
	}

	slices.Sort(table)

	if table == nil {
		table = []string{}
	}

	id := unitID(b.file, "/paths")
	b.add(&node{unit: b.unit(id, KindTop, false), own: digestOf(table), text: digestOf(nil)})

	return id
}

// linkRoutes links every operation to every document's routing table: which
// documents share a router can't be read from the documents.
func (m *Model) linkRoutes() {
	tables := m.RouteUnits()

	for _, op := range m.Operations {
		own := unitID(op.File, "/paths")

		for _, t := range tables {
			if t != own {
				m.Links = append(m.Links, Link{From: op.ID, To: t, Kind: LinkUses})
			}
		}
	}
}

// Contracts lists the files read that are documents themselves, in the order
// they were read.
func (m *Model) Contracts() []string {
	var out []string

	for _, f := range m.files {
		if _, ok := m.top[f]; ok {
			out = append(out, f)
		}
	}

	return out
}

// RouteUnits lists the routing-table unit of every document, in the order
// the documents were read.
func (m *Model) RouteUnits() []string {
	out := make([]string, 0, len(m.top))

	for _, f := range m.Contracts() {
		out = append(out, unitID(f, "/paths"))
	}

	return out
}

func (b *builder) add(n *node) {
	if _, ok := b.nodes[n.unit.ID]; ok {
		return
	}

	b.nodes[n.unit.ID] = n
	b.order = append(b.order, n.unit.ID)
}

func (b *builder) document(d Document, m *Model) ([]Operation, error) {
	root, err := b.l.load(d.Path)
	if err != nil {
		return nil, err
	}

	doc, ok := root.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: %s is not a mapping", ErrInvalid, d.Path)
	}

	var ops []Operation

	if isDocument(doc) {
		b.doc = true
		m.top[d.Path] = b.topUnits(doc)
		m.Servers[d.Path] = serverURLs(doc)

		b.components(doc)

		ops, err = b.operations(d, doc)
		if err != nil {
			return nil, err
		}

		m.top[d.Path] = append(m.top[d.Path], b.routes(ops))
	} else {
		b.fragments(doc)
	}

	if err := errors.Join(b.errs...); err != nil {
		return nil, err
	}

	b.finish(m, ops)

	return ops, nil
}

func isDocument(doc map[string]any) bool {
	_, v3 := doc["openapi"]
	_, v2 := doc["swagger"]

	return v3 || v2
}

func serverURLs(doc map[string]any) []string {
	list := asList(doc["servers"])

	var out []string

	for _, s := range list {
		if m, ok := s.(map[string]any); ok {
			if u, ok := m["url"].(string); ok {
				out = append(out, u)
			}
		}
	}

	return out
}

// topUnits are the parts of a document every operation depends on: servers,
// version, dialect and top-level extensions.
func (b *builder) topUnits(doc map[string]any) []string {
	n := b.normalizer()
	servers := n.listOf(orEmpty(doc["servers"]), ctxServer)

	head := map[string]any{"openapi": doc["openapi"], "swagger": doc["swagger"], "jsonSchemaDialect": doc["jsonSchemaDialect"]}

	for k, v := range doc {
		if strings.HasPrefix(k, "x-") {
			head[k] = v
		}
	}

	ids := []string{unitID(b.file, "/servers"), unitID(b.file, "/openapi")}
	text := map[string]any{"info": doc["info"], "tags": doc["tags"], keyExternalDocs: doc[keyExternalDocs]}
	b.add(&node{unit: b.unit(ids[0], KindTop, false), own: digestOf(servers), text: digestOf(doc["servers"])})
	b.add(&node{unit: b.unit(ids[1], KindTop, false), own: digestOf(head), text: digestOf(text)})
	b.errs = append(b.errs, n.errs...)

	return ids
}

func (b *builder) unit(id, kind string, executable bool) Unit {
	return Unit{ID: id, Group: b.file, File: b.file, Kind: kind, Executable: executable}
}

func (b *builder) normalizer() *normalizer {
	return &normalizer{file: b.file, keepDocs: b.opts.KeepDocs}
}

func orEmpty(v any) any {
	if v == nil {
		return []any{}
	}

	return v
}

// components makes a unit of every entry of every components section, used
// or not, so a report can name a changed schema even when nothing uses it.
func (b *builder) components(doc map[string]any) {
	comps := asMap(doc[keyComponents])

	for _, section := range sortedKeys(comps) {
		ctx, known := sectionContext[section]
		if !known {
			continue
		}

		entries := asMap(comps[section])
		for _, name := range sortedKeys(entries) {
			b.entry("/components/"+section+"/"+escape(name), entries[name], ctx, string(ctx))
		}
	}
}

// fragments makes a unit of every top-level entry of a file of parts. Its
// shape decides its kind; one that fits none is kept whole, which can only
// select more.
func (b *builder) fragments(doc map[string]any) {
	for _, key := range sortedKeys(doc) {
		if key == keyComponents {
			b.components(doc)

			continue
		}

		b.entry("/"+escape(key), doc[key], guessContext(doc[key]), KindFragment)
	}
}

func guessContext(v any) context {
	m, ok := v.(map[string]any)
	if !ok {
		return ctxGeneric
	}

	has := func(keys ...string) bool {
		for _, k := range keys {
			if _, ok := m[k]; ok {
				return true
			}
		}

		return false
	}

	switch {
	case has("in") && has("name"):
		return ctxParameter
	case has("type", "properties", "items", "allOf", "oneOf", "anyOf", "enum", keyRef, "additionalProperties"):
		return ctxSchema
	case has("get", "put", "post", "delete", "patch", "head", "options", "trace"):
		return ctxPathItem
	default:
		return ctxGeneric
	}
}

func (b *builder) entry(ptr string, v any, ctx context, kind string) {
	n := b.normalizer()
	form := n.node(v, ctx)
	edges, external := b.split(n)
	b.add(&node{unit: b.unit(unitID(b.file, ptr), kind, false), own: digestOf(form), edges: edges, external: external, text: digestOf(v)})
}

// split sorts a normaliser's refs into units of this file, which digests
// close over, and units of other files, which only links reach.
func (b *builder) split(n *normalizer) (local, external []string) {
	b.errs = append(b.errs, n.errs...)

	for _, e := range n.edges {
		ptr, ok := unitPointer(e.to.Ptr)
		if !ok {
			if !e.soft {
				b.errs = append(b.errs, fmt.Errorf("%w: $ref %s points inside a path", ErrCannotTell, e.to))
			}

			continue
		}

		id := unitID(e.to.File, ptr)
		if e.to.File != b.file {
			if b.doc && !e.soft {
				b.check(e.to)
			}

			external = append(external, id)

			continue
		}

		if !b.exists(e.to.Ptr) {
			if !e.soft {
				b.errs = append(b.errs, fmt.Errorf("%w: $ref %s: no such node", ErrInvalid, e.to))
			}

			continue
		}

		local = append(local, id)
	}

	slices.Sort(local)
	slices.Sort(external)

	return slices.Compact(local), slices.Compact(external)
}

// check makes sure a ref's target exists: a missing file is "can't tell", a
// missing node an error.
func (b *builder) check(to ref) {
	doc, err := b.l.load(to.File)
	if err != nil {
		b.errs = append(b.errs, fmt.Errorf("$ref %s in %s: %w", to, b.file, missing(err)))

		return
	}

	if _, ok := pointer(doc, to.Ptr); !ok {
		b.errs = append(b.errs, fmt.Errorf("%w: $ref %s in %s: no such node", ErrInvalid, to, b.file))
	}
}

func (b *builder) exists(ptr string) bool {
	doc, err := b.l.load(b.file)
	if err != nil {
		return false
	}

	_, ok := pointer(doc, ptr)

	return ok
}

// unitPointer names the unit a ref lands in. A ref deeper into a unit binds
// the whole unit, which can only select more; refs into paths have no unit.
func unitPointer(ptr string) (string, bool) {
	if ptr == "" {
		return "", true
	}

	parts := strings.Split(strings.TrimPrefix(ptr, "/"), "/")

	switch {
	case parts[0] == keyComponents && len(parts) >= 3:
		return "/" + strings.Join(parts[:3], "/"), true
	case parts[0] == "paths" || parts[0] == "webhooks" || parts[0] == keyComponents:
		return "", false
	default:
		return "/" + parts[0], true
	}
}

func unitID(file, ptr string) string {
	if ptr == "" {
		return Namespace + ":" + file
	}

	return Namespace + ":" + file + "#" + ptr
}

func (b *builder) finish(m *Model, ops []Operation) {
	whole, wholeText := sha256.New(), sha256.New()

	var external []string

	for _, id := range b.order {
		n := b.nodes[id]

		h := sha256.New()
		h.Write([]byte(n.own))

		for _, r := range b.closure(id) {
			h.Write([]byte("\n" + r + " " + b.nodes[r].own))
		}

		n.unit.Digest = hex.EncodeToString(h.Sum(nil))
		m.Units = append(m.Units, n.unit)
		m.byDoc[b.file] = append(m.byDoc[b.file], id)
		whole.Write([]byte(id + " " + n.unit.Digest + "\n"))
		wholeText.Write([]byte(id + " " + n.text + "\n"))
		b.docs(m, id, n.text)

		for _, to := range n.edges {
			if to != id {
				m.Links = append(m.Links, Link{From: id, To: to, Kind: LinkUses})
			}
		}

		for _, to := range n.external {
			m.Links = append(m.Links, Link{From: id, To: to, Kind: LinkUses})
		}

		external = append(external, n.external...)
	}

	for _, op := range ops {
		for _, t := range m.top[b.file] {
			m.Links = append(m.Links, Link{From: op.ID, To: t, Kind: LinkUses})
		}
	}

	// The whole file: what a test that reads the file depends on.
	docID := unitID(b.file, "")
	m.Units = append(m.Units, Unit{ID: docID, Group: b.file, File: b.file, Kind: KindDocument, Digest: hex.EncodeToString(whole.Sum(nil))})
	m.byDoc[b.file] = append(m.byDoc[b.file], docID)
	b.docs(m, docID, hex.EncodeToString(wholeText.Sum(nil)))

	// While document-units is on, the core binds a test that depends on the file
	// to the units of it the test touched instead.
	for _, u := range m.Units {
		if u.File == b.file {
			m.Links = append(m.Links, Link{From: "file:" + b.file, To: u.ID, Kind: LinkChanges})
		}
	}

	slices.Sort(external)

	for _, to := range slices.Compact(external) {
		m.Links = append(m.Links, Link{From: docID, To: to, Kind: LinkUses})
	}
}

func (b *builder) docs(m *Model, id, text string) {
	if b.opts.KeepDocs {
		return
	}

	m.Units = append(m.Units, Unit{ID: DocsID(id), Group: b.file, File: b.file, Kind: KindDocs, Digest: text})
	m.Links = append(m.Links, Link{From: id, To: DocsID(id), Kind: LinkUses, Narrowing: NarrowDocText, While: "off"})
}

// DocumentUnits lists the ids of every unit of a document, the whole-file
// unit included: what binding the document whole means.
func (m *Model) DocumentUnits(file string) []string { return m.byDoc[file] }

// TopUnits lists the ids of a document's top-level units, which every
// operation of it depends on.
func (m *Model) TopUnits(file string) []string { return m.top[file] }

// OperationsOf lists the operations a document's paths and webhooks hold.
func (m *Model) OperationsOf(file string) []Operation {
	var ops []Operation

	for _, op := range m.Operations {
		if op.File == file {
			ops = append(ops, op)
		}
	}

	return ops
}

// closure lists the units of the file id reaches through refs, itself
// excluded, sorted. Refs may form cycles.
func (b *builder) closure(id string) []string {
	seen := map[string]bool{id: true}
	stack := []string{id}

	var out []string

	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		for _, to := range b.nodes[cur].edges {
			if seen[to] {
				continue
			}

			seen[to] = true

			if _, ok := b.nodes[to]; !ok {
				continue
			}

			out = append(out, to)
			stack = append(stack, to)
		}
	}

	slices.Sort(out)

	return out
}

func digestOf(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		// Only a value YAML can't produce fails here; hash its printed
		// form so the unit still changes when it does.
		data = fmt.Appendf(nil, "%#v", v)
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return keys
}
