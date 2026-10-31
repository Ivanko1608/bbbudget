package httpapi

import (
	"net/http"

	"bbbudget/internal/store"
)

type notesBody struct {
	Notes []store.Note `json:"notes"`
}

func (s *server) listNotes(w http.ResponseWriter, r *http.Request) {
	notes, err := s.app.ListNotes(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, notesBody{Notes: notes})
}

func (s *server) deleteNote(w http.ResponseWriter, r *http.Request) {
	if err := s.app.DeleteNote(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
