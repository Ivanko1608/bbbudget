package app

import (
	"context"

	"bbbudget/internal/store"
)

// AddNote saves a note about the person, remembering where it came from.
func (a *App) AddNote(
	ctx context.Context, text string, source store.NoteSource,
) (store.Note, error) {
	return a.Store.AddNote(ctx, text, source)
}

// ListNotes returns the person's notes, oldest first.
func (a *App) ListNotes(ctx context.Context) ([]store.Note, error) {
	return a.Store.ListNotes(ctx)
}

// DeleteNote removes one note.
func (a *App) DeleteNote(ctx context.Context, id string) error {
	return a.Store.DeleteNote(ctx, id)
}
