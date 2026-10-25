// Package rules reads every operation of the contract.
package rules

import "example.com/notes/internal/spec"

// Count counts the paths.
func Count() int {
	doc, err := spec.Notes()
	if err != nil {
		return 0
	}

	return doc.Paths.Len()
}
