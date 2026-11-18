// Package access says who may call each operation.
package access

// Roles names the role each operation needs, by operationId.
var Roles = map[string]string{
	"getNote":    "reader",
	"listNotes":  "reader",
	"createNote": "writer",
}

// Labels mixes an operation with other keys, so it isn't keyed by
// operations.
var Labels = map[string]string{"getNote": "Read", "draft": "Draft"}

// Only names one operation, as code about that operation alone does.
var Only = map[string]bool{"getNote": true}

// RoleOf looks a role up.
func RoleOf(op string) string { return Roles[op] }
