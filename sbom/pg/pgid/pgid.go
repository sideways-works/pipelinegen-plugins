// Package pgid names the objects of a PostgreSQL database as pipelinegen
// units, for every plugin that links to or observes them. Names are as the
// catalog stores them: unquoted folded to lower case, quoted as written.
package pgid

import "strings"

// Kinds of unit, the second part of an id.
const (
	Table     = "table"
	View      = "view"
	Function  = "function"
	Type      = "type"
	Sequence  = "sequence"
	Extension = "extension"
	// Data is the rows the migrations left in a table: a test that reads
	// the table depends on them, apart from the table's definition.
	Data = "data"
	// Relation names a table or a view, for code that can't tell which.
	Relation = "relation"
	// RoutineKind names every overload of a function or procedure, for a
	// call whose argument types aren't known.
	RoutineKind = "routine"
)

// Of builds pg:<kind>:<schema>.<name>.
func Of(kind, schema, name string) string {
	return "pg:" + kind + ":" + schema + "." + name
}

// Func names one function by its argument types, as PostgreSQL prints
// them: pg:function:public.f(integer, text).
func Func(schema, name string, args []string) string {
	return Of(Function, schema, name) + "(" + strings.Join(args, ", ") + ")"
}

// Routine names every overload of a function or procedure at once, for a
// call whose argument types aren't known: pg:routine:public.f.
func Routine(schema, name string) string {
	return Of(RoutineKind, schema, name)
}

// Ext names an extension: pg:extension:citext. Extensions belong to no
// schema of their own.
func Ext(name string) string {
	return "pg:" + Extension + ":" + name
}

// Parse splits an id into its kind and the rest (schema.name, possibly
// with arguments). ok is false for an id outside the namespace.
func Parse(id string) (kind, rest string, ok bool) {
	after, found := strings.CutPrefix(id, "pg:")
	if !found {
		return "", "", false
	}

	return strings.Cut(after, ":")
}

// MaxLabel is the longest label: one line of pipelinegen's why.
const MaxLabel = 80

// Label names an object as people do (the table bookings, the function
// f(integer)). It is for people only: nothing compares, hashes or caches it.
func Label(id string) string {
	kind, rest, ok := Parse(id)
	if !ok {
		return clip(id)
	}

	name := strings.TrimPrefix(rest, "public.")

	switch kind {
	case Data:
		return clip("the rows of table " + name)
	case Relation:
		return clip("the table or view " + name)
	case RoutineKind:
		return clip("every overload of " + name)
	default:
		return clip("the " + kind + " " + name)
	}
}

func clip(s string) string {
	r := []rune(s)
	if len(r) <= MaxLabel {
		return s
	}

	half := (MaxLabel - 1) / 2

	return string(r[:half]) + "…" + string(r[len(r)-(MaxLabel-1-half):])
}
