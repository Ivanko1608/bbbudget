package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bbbudget/internal/app"
	"bbbudget/internal/store"
)

type noteClient struct {
	*client
	app *app.App
}

func setupNotes(t *testing.T) noteClient {
	t.Helper()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(st, nil)
	return noteClient{client: &client{t, New(a, Options{})}, app: a}
}

func (c noteClient) addNote(text string, source store.NoteSource) store.Note {
	c.t.Helper()
	note, err := c.app.AddNote(c.t.Context(), text, source)
	if err != nil {
		c.t.Fatal(err)
	}
	return note
}

func (c noteClient) listNotes() []store.Note {
	c.t.Helper()
	w := c.req("GET", "/api/notes", "", nil)
	eq(c.t, "list status", w.Code, http.StatusOK)
	var body struct {
		Notes []store.Note `json:"notes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		c.t.Fatalf("list body: %v %s", err, w.Body)
	}
	return body.Notes
}

func TestListNotesShowsTextSourceAndDate(t *testing.T) {
	c := setupNotes(t)
	added := c.addNote("works as a freelance designer", store.NoteSourceStatementImport)

	w := c.req("GET", "/api/notes", "", nil)

	eq(t, "status", w.Code, http.StatusOK)
	var raw struct {
		Notes []map[string]any `json:"notes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	eq(t, "notes", len(raw.Notes), 1)
	eq(t, "id", raw.Notes[0]["id"], any(added.ID))
	eq(t, "text", raw.Notes[0]["text"], "works as a freelance designer")
	eq(t, "source", raw.Notes[0]["source"], any(string(store.NoteSourceStatementImport)))
	eq(t, "date", raw.Notes[0]["created_at"], any(added.CreatedAt.Format(time.RFC3339Nano)))
}

func TestListNotesWithNoneIsAnEmptyList(t *testing.T) {
	c := setupNotes(t)

	w := c.req("GET", "/api/notes", "", nil)

	eq(t, "status", w.Code, http.StatusOK)
	eq(t, "body", strings.TrimSpace(w.Body.String()), `{"notes":[]}`)
}

func TestDeleteNoteRemovesIt(t *testing.T) {
	c := setupNotes(t)
	note := c.addNote("going through a divorce", store.NoteSourceChat)

	w := c.req("DELETE", "/api/notes/"+note.ID, "", nil)

	eq(t, "status", w.Code, http.StatusNoContent)
	eq(t, "notes left", len(c.listNotes()), 0)
}

func TestDeleteNoteNeedsTheRequestedWithHeader(t *testing.T) {
	c := setupNotes(t)
	note := c.addNote("going through a divorce", store.NoteSourceChat)

	w := c.req("DELETE", "/api/notes/"+note.ID, "", func(r *http.Request) {
		r.Header.Del("X-Requested-With")
	})

	eq(t, "status", w.Code, http.StatusForbidden)
	eq(t, "notes left", len(c.listNotes()), 1)
}

func TestDeleteNoteIsRefusedOrNotFound(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want int
	}{
		{name: "unknown id", id: "0123456789abcdef", want: http.StatusNotFound},
		{name: "too short", id: "nope", want: http.StatusBadRequest},
		{name: "upper case", id: "0123456789ABCDEF", want: http.StatusBadRequest},
		{name: "not hex", id: "0123456789abcdeg", want: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := setupNotes(t)

			w := c.req("DELETE", "/api/notes/"+tt.id, "", nil)

			eq(t, "status", w.Code, tt.want)
		})
	}
}

func TestAnAppFromNewServesNotes(t *testing.T) {
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := &client{t, New(app.New(st, nil), Options{})}

	w := c.req("GET", "/api/notes", "", nil)

	eq(t, "status", w.Code, http.StatusOK)
}

type budgetsOnly struct{ store.Store }

func TestAnAppWithoutNotesAnswersNotFound(t *testing.T) {
	c := &client{t, New(app.New(budgetsOnly{}, nil), Options{})}

	list := c.req("GET", "/api/notes", "", nil)
	remove := c.req("DELETE", "/api/notes/0123456789abcdef", "", nil)

	eq(t, "list status", list.Code, http.StatusNotFound)
	eq(t, "delete status", remove.Code, http.StatusNotFound)
}

func TestNotesOfOneAppAreNotVisibleToAnother(t *testing.T) {
	mine := setupNotes(t)
	yours := setupNotes(t)
	note := mine.addNote("works as a freelance designer", store.NoteSourceChat)

	w := yours.req("DELETE", "/api/notes/"+note.ID, "", nil)

	eq(t, "notes listed for the other app", len(yours.listNotes()), 0)
	eq(t, "other app delete status", w.Code, http.StatusNotFound)
	eq(t, "notes still listed for the first app", len(mine.listNotes()), 1)
}

type failingNotes struct{ store.NoteStore }

func (failingNotes) ListNotes(context.Context) ([]store.Note, error) {
	return nil, errors.New("open /srv/bbbudget/.notes.json: disk on fire")
}

func TestListNotesHidesInternalErrors(t *testing.T) {
	c := setupNotes(t)
	c.app.Notes = failingNotes{}

	w := c.req("GET", "/api/notes", "", nil)

	eq(t, "status", w.Code, http.StatusInternalServerError)
	eq(t, "body", strings.TrimSpace(w.Body.String()), `{"error":"internal error"}`)
}

func TestNoteErrorsMapToStatuses(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{store.ErrNoteNotFound, http.StatusNotFound},
		{store.ErrTooManyNotes, http.StatusConflict},
		{store.ErrNoteTooLong, http.StatusBadRequest},
		{store.ErrNoteEmpty, http.StatusBadRequest},
		{store.ErrBadNoteSource, http.StatusBadRequest},
		{store.ErrBadNoteID, http.StatusBadRequest},
		{app.ErrNotesUnavailable, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.err.Error(), func(t *testing.T) {
			s := &server{opts: Options{}}
			w := httptest.NewRecorder()

			s.failNote(w, httptest.NewRequest("GET", "/api/notes", nil), tt.err)

			eq(t, "status", w.Code, tt.want)
			eq(t, "message", strings.Contains(w.Body.String(), tt.err.Error()), true)
		})
	}
}
