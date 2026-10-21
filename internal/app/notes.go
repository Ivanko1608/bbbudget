package app

import (
	"context"
	"errors"

	"bbbudget/internal/store"
)

// ErrNotesUnavailable means the store behind the App keeps no notes.
var ErrNotesUnavailable = errors.New("notes are not available")

// AddNote saves a note about the person, remembering where it came from.
func (a *App) AddNote(
	ctx context.Context, text string, source store.NoteSource,
) (store.Note, error) {
	if a.Notes == nil {
		return store.Note{}, ErrNotesUnavailable
	}
	return a.Notes.AddNote(ctx, text, source)
}

// ListNotes returns the person's notes, oldest first.
func (a *App) ListNotes(ctx context.Context) ([]store.Note, error) {
	if a.Notes == nil {
		return nil, ErrNotesUnavailable
	}
	return a.Notes.ListNotes(ctx)
}

// DeleteNote removes one note.
func (a *App) DeleteNote(ctx context.Context, id string) error {
	if a.Notes == nil {
		return ErrNotesUnavailable
	}
	return a.Notes.DeleteNote(ctx, id)
}
