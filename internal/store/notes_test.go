package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func checkEqual[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func newNoteStore(t *testing.T) *FileStore {
	t.Helper()
	s, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func addNotes(t *testing.T, s *FileStore, count int) {
	t.Helper()
	for range count {
		if _, err := s.AddNote(ctx, "works as a freelance designer", NoteSourceChat); err != nil {
			t.Fatal(err)
		}
	}
}

func listNotes(t *testing.T, s *FileStore) []Note {
	t.Helper()
	notes, err := s.ListNotes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return notes
}

func TestANoteRecordsItsTextSourceAndDate(t *testing.T) {
	s := newNoteStore(t)
	savedAt := time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC)
	s.now = func() time.Time { return savedAt }

	added, err := s.AddNote(ctx, "  works as a freelance designer ", NoteSourceStatementImport)
	if err != nil {
		t.Fatal(err)
	}
	listed := listNotes(t, s)

	if len(listed) != 1 {
		t.Fatalf("got %d notes, want 1", len(listed))
	}
	checkEqual(t, "listed note", listed[0], added)
	checkEqual(t, "text", added.Text, "works as a freelance designer")
	checkEqual(t, "source", added.Source, NoteSourceStatementImport)
	checkEqual(t, "date", added.CreatedAt, savedAt)
	checkEqual(t, "id is set", added.ID != "", true)
}

func TestNotesSurviveReopeningTheStore(t *testing.T) {
	dir := t.TempDir()
	first, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = first.AddNote(ctx, "moving house in March", NoteSourceChat); err != nil {
		t.Fatal(err)
	}

	second, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	checkEqual(t, "notes", len(listNotes(t, second)), 1)
}

func TestAStoreWithoutNotesListsNone(t *testing.T) {
	s := newNoteStore(t)

	notes := listNotes(t, s)

	checkEqual(t, "notes", len(notes), 0)
	checkEqual(t, "notes is an empty list, not nil", notes != nil, true)
}

func TestDeletingTheLastNoteLeavesAnEmptyList(t *testing.T) {
	s := newNoteStore(t)
	note, err := s.AddNote(ctx, "works as a freelance designer", NoteSourceChat)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteNote(ctx, note.ID); err != nil {
		t.Fatal(err)
	}

	notes := listNotes(t, s)
	checkEqual(t, "notes", len(notes), 0)
	checkEqual(t, "notes is an empty list, not nil", notes != nil, true)
}

func TestNotesDoNotShowUpAsBudgets(t *testing.T) {
	s := newNoteStore(t)
	addNotes(t, s, 1)

	metas, err := s.List(ctx)

	if err != nil {
		t.Fatal(err)
	}
	checkEqual(t, "budgets", len(metas), 0)
}

func TestAddNoteRefusesBadNotes(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		source NoteSource
		want   error
	}{
		{name: "empty", text: "", source: NoteSourceChat, want: ErrNoteEmpty},
		{name: "only spaces", text: "   ", source: NoteSourceChat, want: ErrNoteEmpty},
		{
			name:   "201 characters",
			text:   strings.Repeat("a", MaxNoteRunes+1),
			source: NoteSourceChat,
			want:   ErrNoteTooLong,
		},
		{
			name:   "201 runes",
			text:   strings.Repeat("я", MaxNoteRunes+1),
			source: NoteSourceChat,
			want:   ErrNoteTooLong,
		},
		{
			name:   "unknown source",
			text:   "hello",
			source: NoteSource("telepathy"),
			want:   ErrBadNoteSource,
		},
		{name: "no source", text: "hello", source: NoteSource(""), want: ErrBadNoteSource},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newNoteStore(t)

			_, err := s.AddNote(ctx, tt.text, tt.source)

			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			checkEqual(t, "notes saved", len(listNotes(t, s)), 0)
		})
	}
}

func TestAddNoteAcceptsTheLongestNote(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{"200 ASCII characters", strings.Repeat("a", MaxNoteRunes)},
		{"200 Cyrillic characters, 400 bytes", strings.Repeat("я", MaxNoteRunes)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newNoteStore(t)

			_, err := s.AddNote(ctx, tt.text, NoteSourceChat)

			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestThe51stNoteIsRefused(t *testing.T) {
	s := newNoteStore(t)
	addNotes(t, s, MaxNotes)

	_, err := s.AddNote(ctx, "one too many", NoteSourceChat)

	if !errors.Is(err, ErrTooManyNotes) {
		t.Fatalf("err = %v, want %v", err, ErrTooManyNotes)
	}
	checkEqual(t, "notes kept", len(listNotes(t, s)), MaxNotes)
}

func TestDeletingANoteMakesRoomForAnother(t *testing.T) {
	s := newNoteStore(t)
	addNotes(t, s, MaxNotes)

	if err := s.DeleteNote(ctx, listNotes(t, s)[0].ID); err != nil {
		t.Fatal(err)
	}
	_, err := s.AddNote(ctx, "fits now", NoteSourceChat)

	if err != nil {
		t.Fatal(err)
	}
}

func TestDeleteNoteRemovesOnlyThatNote(t *testing.T) {
	s := newNoteStore(t)
	keep, err := s.AddNote(ctx, "keep me", NoteSourceChat)
	if err != nil {
		t.Fatal(err)
	}
	drop, err := s.AddNote(ctx, "drop me", NoteSourceConnector)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteNote(ctx, drop.ID); err != nil {
		t.Fatal(err)
	}
	left := listNotes(t, s)

	if len(left) != 1 {
		t.Fatalf("got %d notes left, want 1", len(left))
	}
	checkEqual(t, "note left", left[0].ID, keep.ID)
}

func TestDeleteNoteRefusesAnUnknownNote(t *testing.T) {
	s := newNoteStore(t)

	err := s.DeleteNote(ctx, "0123456789abcdef")

	if !errors.Is(err, ErrNoteNotFound) {
		t.Fatalf("err = %v, want %v", err, ErrNoteNotFound)
	}
}

func TestNotesOfOneStoreAreNotInAnother(t *testing.T) {
	mine := newNoteStore(t)
	yours := newNoteStore(t)
	note, err := mine.AddNote(ctx, "works as a freelance designer", NoteSourceChat)
	if err != nil {
		t.Fatal(err)
	}

	deleteErr := yours.DeleteNote(ctx, note.ID)

	checkEqual(t, "notes seen by the other store", len(listNotes(t, yours)), 0)
	notFound := errors.Is(deleteErr, ErrNoteNotFound)
	checkEqual(t, "other store refuses it as not found", notFound, true)
	checkEqual(t, "notes of the first store", len(listNotes(t, mine)), 1)
}

func TestConcurrentAddsNeverPassTheLimit(t *testing.T) {
	s := newNoteStore(t)
	const attempts = MaxNotes + 10
	results := make(chan error, attempts)

	for range attempts {
		go func() {
			_, err := s.AddNote(ctx, "racing", NoteSourceChat)
			results <- err
		}()
	}
	refused := 0
	for range attempts {
		err := <-results
		if errors.Is(err, ErrTooManyNotes) {
			refused++
		} else if err != nil {
			t.Fatal(err)
		}
	}

	checkEqual(t, "refused", refused, 10)
	checkEqual(t, "kept", len(listNotes(t, s)), MaxNotes)
}

func TestDeleteNoteRefusesAMalformedID(t *testing.T) {
	ids := []string{"", "nope", "0123456789ABCDEF", "0123456789abcde", "../budgets/home"}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			s := newNoteStore(t)

			err := s.DeleteNote(ctx, id)

			if !errors.Is(err, ErrBadNoteID) {
				t.Fatalf("err = %v, want %v", err, ErrBadNoteID)
			}
		})
	}
}
