// Package analysis finds statically which PostgreSQL relations Go code
// reaches through GORM. Where the code doesn't say which relation a call
// reaches, the declaration is reported as "cannot tell", never guessed.
package analysis

import (
	"os"
	"strings"

	"github.com/sideways-works/pipelinegen-plugins/sbom/pg/pgid"

	"gorm.io/gorm/schema"
)

type Config struct {
	// Modules are the Go module directories to load; by default every module
	// whose go.mod requires GORM.
	Modules []string `json:"modules,omitempty"`
	// Tests includes test files (the default): tests that query tables
	// directly are users of those tables too.
	Tests *bool `json:"tests,omitempty"`
	// Schema is the schema an unqualified name resolves to; the default is
	// public.
	Schema string `json:"schema,omitempty"`
	// Naming is the GORM naming strategy the application configures.
	Naming Naming `json:"naming"`
	// Build is the build context the code is read under; the default is the Go
	// inventory's.
	Build Build `json:"build"`
}

// Build fixes what would otherwise vary between machines, so an answer
// depends only on the tree and the config.
type Build struct {
	GOOS   string `json:"goos,omitempty"`
	GOARCH string `json:"goarch,omitempty"`
	// Cgo is CGO_ENABLED; off unless set.
	Cgo bool `json:"cgo,omitempty"`
	// Tags are the build tags; nil is DefaultTags.
	Tags []string `json:"tags,omitempty"`
}

// DefaultTags are the build tags of a repository that names none, as the Go
// inventory reads the code.
var DefaultTags = []string{"pipelinegen_cover"}

func (b Build) env() []string {
	goos, goarch, cgo := "linux", "amd64", "0"
	if b.GOOS != "" {
		goos = b.GOOS
	}

	if b.GOARCH != "" {
		goarch = b.GOARCH
	}

	if b.Cgo {
		cgo = "1"
	}

	return append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED="+cgo)
}

func (b Build) flags() []string {
	tags := b.Tags
	if tags == nil {
		tags = DefaultTags
	}

	if len(tags) == 0 {
		return nil
	}

	return []string{"-tags=" + strings.Join(tags, ",")}
}

// Naming mirrors the fields of GORM's schema.NamingStrategy that decide
// table and column names.
type Naming struct {
	TablePrefix   string `json:"table_prefix,omitempty"`
	SingularTable bool   `json:"singular_table,omitempty"`
	NoLowerCase   bool   `json:"no_lower_case,omitempty"`
}

func (n Naming) strategy() schema.NamingStrategy {
	return schema.NamingStrategy{TablePrefix: n.TablePrefix, SingularTable: n.SingularTable, NoLowerCase: n.NoLowerCase}
}

func (c Config) tests() bool {
	return c.Tests == nil || *c.Tests
}

func (c Config) schema() string {
	if c.Schema == "" {
		return "public"
	}

	return c.Schema
}

// The narrowings this correlator implements. The wider rule's links go out
// tagged to hold only while the narrowing is off.
const (
	// NarrowModels takes a statement's table from Table() alone when set, and
	// links only the hooks GORM calls for that kind of statement.
	NarrowModels = "gorm-models"
	// NarrowAssociations leaves out the associations GORM saves with a model
	// when the value is a literal that sets none of them.
	NarrowAssociations = "gorm-associations"
	// NarrowModelTypes leaves out the links from a model's type to its table:
	// a call site links to the table on its own.
	NarrowModelTypes = "gorm-model-types"
)

// WhileOff tags a wide link that stands only while its narrowing is off.
const WhileOff = "off"

// Relation is a table, view or materialised view by schema and name, as
// PostgreSQL folds the names.
type Relation struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
}

// RelationPrefix starts a relation's id, which the database inventory gives
// tables, views and materialised views alike.
const RelationPrefix = "pg:" + pgid.Relation + ":"

func (r Relation) ID() string {
	return pgid.Of(pgid.Relation, r.Schema, r.Name)
}

// RoutinePrefix starts the id naming every overload of a routine: a call's
// argument types aren't known from its text.
const RoutinePrefix = "pg:" + pgid.RoutineKind + ":"

// RoutineID is the id of the routines named like r.
func (r Relation) RoutineID() string {
	return pgid.Routine(r.Schema, r.Name)
}

func (c Config) relation(name string) Relation {
	if s, n, ok := strings.Cut(name, "."); ok && !strings.Contains(n, ".") {
		return Relation{Schema: s, Name: n}
	}

	return Relation{Schema: c.schema(), Name: name}
}
