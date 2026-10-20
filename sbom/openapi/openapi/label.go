package openapi

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxLabel is the longest label: one line of pipelinegen's why.
const MaxLabel = 80

func (m *Model) label() {
	ops := make(map[string]Operation, len(m.Operations))
	for _, op := range m.Operations {
		ops[op.ID] = op
	}

	for i := range m.Units {
		m.Units[i].Label = labeler(m.Units[i], ops)
	}
}

// labeler names a unit; a variable, so a test can show nothing else in an
// answer depends on it.
var labeler = labelOf

func labelOf(u Unit, ops map[string]Operation) string {
	if rest, ok := strings.CutPrefix(u.ID, Namespace+":docs:"); ok {
		base := Namespace + ":" + rest

		return Clip("the text of " + labelOf(Unit{ID: base, File: u.File}, ops))
	}

	if op, ok := ops[u.ID]; ok {
		if name, hook := strings.CutPrefix(op.Path, webhookPrefix); hook {
			return Clip("webhook " + name + " " + strings.ToLower(op.Method) + " (" + op.OperationID + ")")
		}

		return Clip(op.Method + " " + op.Path + " (" + op.OperationID + ")")
	}

	_, ptr, _ := strings.Cut(strings.TrimPrefix(u.ID, Namespace+":"), "#")
	parts := strings.Split(strings.TrimPrefix(ptr, "/"), "/")

	for i, p := range parts {
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(p, "~1", "/"), "~0", "~")
	}

	switch {
	case ptr == "":
		return Clip("the document " + u.File)
	case ptr == "/servers":
		return Clip("the servers of " + u.File)
	case ptr == "/openapi":
		return Clip("the version of " + u.File)
	case ptr == "/paths" && u.Kind == KindTop:
		return Clip("the routes of " + u.File)
	case len(parts) == 3 && parts[0] == keyComponents:
		return Clip(singular(parts[1]) + " " + parts[2] + " in " + u.File)
	default:
		return Clip(strings.Join(parts, "/") + " in " + u.File)
	}
}

func singular(section string) string {
	ctx, ok := sectionContext[section]
	if !ok {
		return section
	}

	var b strings.Builder

	for _, r := range string(ctx) {
		if unicode.IsUpper(r) {
			b.WriteByte(' ')

			r = unicode.ToLower(r)
		}

		b.WriteRune(r)
	}

	return b.String()
}

// Clip keeps a label within MaxLabel characters, cutting its middle.
func Clip(s string) string {
	if utf8.RuneCountInString(s) <= MaxLabel {
		return s
	}

	r := []rune(s)
	half := (MaxLabel - 1) / 2

	return string(r[:half]) + "…" + string(r[len(r)-(MaxLabel-1-half):])
}
