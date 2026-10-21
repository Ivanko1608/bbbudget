package httpapi

import (
	"errors"
	"net/http"

	"bbbudget/internal/app"
	"bbbudget/internal/store"
)

type notesBody struct {
	Notes []store.Note `json:"notes"`
}

func (s *server) listNotes(w http.ResponseWriter, r *http.Request) {
	notes, err := s.app.ListNotes(r.Context())
	if err != nil {
		s.failNote(w, r, err)
		return
	}
	if notes == nil {
		notes = []store.Note{}
	}
	writeJSON(w, http.StatusOK, notesBody{Notes: notes})
}

func (s *server) deleteNote(w http.ResponseWriter, r *http.Request) {
	if err := s.app.DeleteNote(r.Context(), r.PathValue("id")); err != nil {
		s.failNote(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// failNote maps the note errors to statuses and leaves everything else to fail.
func (s *server) failNote(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNoteNotFound), errors.Is(err, app.ErrNotesUnavailable):
		writeErr(w, http.StatusNotFound, err.Error(), nil)
	case errors.Is(err, store.ErrTooManyNotes):
		writeErr(w, http.StatusConflict, err.Error(), nil)
	case errors.Is(err, store.ErrNoteEmpty),
		errors.Is(err, store.ErrNoteTooLong),
		errors.Is(err, store.ErrBadNoteSource),
		errors.Is(err, store.ErrBadNoteID):
		writeErr(w, http.StatusBadRequest, err.Error(), nil)
	default:
		s.fail(w, r, err)
	}
}
