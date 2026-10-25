// Package handlers implements the notes operations.
package handlers

import (
	"context"

	"example.com/notes/gen/notesgen"
)

// Notes serves the notes operations.
type Notes struct{}

// ListNotes lists notes.
func (n *Notes) ListNotes(ctx context.Context, req notesgen.ListNotesRequestObject) (notesgen.ListNotesResponseObject, error) {
	return nil, nil
}

// GetNote reads one note.
func (n *Notes) GetNote(ctx context.Context, req notesgen.GetNoteRequestObject) (notesgen.GetNoteResponseObject, error) {
	return nil, nil
}

// Helper takes a request object but isn't named after its operation.
func (n *Notes) Helper(req notesgen.GetNoteRequestObject) {}
