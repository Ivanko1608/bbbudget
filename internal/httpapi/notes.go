package httpapi

import (
	"errors"
	"net/http"

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
	writeJSON(w, http.StatusOK, notesBody{Notes: notes})
}

func (s *server) deleteNote(w http.ResponseWriter, r *http.Request) {
	if err := s.app.DeleteNote(r.Context(), r.PathValue("id")); err != nil {
		s.failNote(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type noteError struct {
	sentinel error
	status   int
}

var noteErrors = []noteError{
	{sentinel: store.ErrNoteNotFound, status: http.StatusNotFound},
	{sentinel: store.ErrTooManyNotes, status: http.StatusConflict},
	{sentinel: store.ErrNoteEmpty, status: http.StatusBadRequest},
	{sentinel: store.ErrNoteTooLong, status: http.StatusBadRequest},
	{sentinel: store.ErrBadNoteSource, status: http.StatusBadRequest},
	{sentinel: store.ErrBadNoteID, status: http.StatusBadRequest},
}

// failNote answers a note error with the sentinel's own message, never with
// the wrapped text, and leaves everything else to fail.
func (s *server) failNote(w http.ResponseWriter, r *http.Request, err error) {
	for _, known := range noteErrors {
		if errors.Is(err, known.sentinel) {
			writeErr(w, known.status, known.sentinel.Error(), nil)
			return
		}
	}
	s.fail(w, r, err)
}
