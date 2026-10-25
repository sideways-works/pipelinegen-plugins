// Package spec hands out the decoded contract once.
package spec

import (
	"sync"

	"example.com/notes/gen/notesgen"
)

// Notes only wraps the generated accessor.
var Notes = sync.OnceValues(notesgen.GetSpec)
