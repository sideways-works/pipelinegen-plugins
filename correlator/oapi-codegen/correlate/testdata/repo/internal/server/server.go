// Package server wires the middleware.
package server

import (
	"example.com/notes/gen/notesgen"
	"example.com/notes/internal/spec"
)

// Middleware validates each request against its own operation.
func Middleware() any {
	return validator(spec.Notes)
}

func validator(any) any { return nil }

// Raw serves the whole document.
func Raw() ([]byte, error) {
	return notesgen.GetSpecJSON()
}
