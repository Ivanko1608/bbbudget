package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// NoteSource is where a note came from. It decides how the note was saved
// and is shown next to the note for as long as it exists.
type NoteSource string

const (
	// NoteSourceChat is the person's own chat message.
	NoteSourceChat NoteSource = "chat"
	// NoteSourceStatementImport is a pasted or imported statement.
	NoteSourceStatementImport NoteSource = "statement_import"
	// NoteSourceConnector is another app, over the connector.
	NoteSourceConnector NoteSource = "connector"
)

var noteSources = []NoteSource{NoteSourceChat, NoteSourceStatementImport, NoteSourceConnector}

const (
	// MaxNotes is how many notes an account keeps.
	MaxNotes = 50
	// MaxNoteRunes is the longest a note's text may be, counted in Unicode
	// characters, so a note in any language gets the same room.
	MaxNoteRunes = 200
)

var (
	ErrNoteNotFound  = errors.New("note not found")
	ErrBadNoteID     = errors.New("note id must be 16 lowercase hex characters")
	ErrNoteEmpty     = errors.New("a note needs some text")
	ErrNoteTooLong   = fmt.Errorf("a note is at most %d characters", MaxNoteRunes)
	ErrTooManyNotes  = fmt.Errorf("at most %d notes can be kept; delete one first", MaxNotes)
	ErrBadNoteSource = errors.New("unknown note source")
)

// Note is one short fact about the person, with where and when it was saved.
type Note struct {
	ID        string     `json:"id"`
	Text      string     `json:"text"`
	Source    NoteSource `json:"source"`
	CreatedAt time.Time  `json:"created_at"`
}

// NoteStore keeps an account's notes.
type NoteStore interface {
	// AddNote saves a note, or returns ErrNoteEmpty, ErrNoteTooLong,
	// ErrBadNoteSource or ErrTooManyNotes without saving anything.
	AddNote(ctx context.Context, text string, source NoteSource) (Note, error)
	// ListNotes returns the notes, oldest first.
	ListNotes(ctx context.Context) ([]Note, error)
	// DeleteNote removes a note, or returns ErrNoteNotFound.
	DeleteNote(ctx context.Context, id string) error
}

// notesFile starts with a dot, so it can never be the file of a budget.
const notesFile = ".notes.json"

type notesDocument struct {
	Notes []Note `json:"notes"`
}

func (s *FileStore) notesPath() string { return filepath.Join(s.dir, notesFile) }

func (s *FileStore) AddNote(_ context.Context, text string, source NoteSource) (Note, error) {
	text = strings.TrimSpace(text)
	if err := validateNote(text, source); err != nil {
		return Note{}, err
	}
	id, err := newNoteID()
	if err != nil {
		return Note{}, err
	}
	note := Note{ID: id, Text: text, Source: source, CreatedAt: s.now().UTC()}
	err = s.withLock(true, func() error {
		notes, readErr := s.readNotes()
		if readErr != nil {
			return readErr
		}
		if len(notes) >= MaxNotes {
			return ErrTooManyNotes
		}
		return s.writeNotes(append(notes, note))
	})
	if err != nil {
		return Note{}, err
	}
	return note, nil
}

func (s *FileStore) ListNotes(_ context.Context) (notes []Note, err error) {
	err = s.withLock(false, func() error {
		notes, err = s.readNotes()
		return err
	})
	return notes, err
}

func (s *FileStore) DeleteNote(_ context.Context, id string) error {
	if !noteIDPattern.MatchString(id) {
		return ErrBadNoteID
	}
	return s.withLock(true, func() error {
		notes, err := s.readNotes()
		if err != nil {
			return err
		}
		index := slices.IndexFunc(notes, func(n Note) bool { return n.ID == id })
		if index < 0 {
			return ErrNoteNotFound
		}
		return s.writeNotes(slices.Delete(notes, index, index+1))
	})
}

var noteIDPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

func validateNote(text string, source NoteSource) error {
	switch {
	case text == "":
		return ErrNoteEmpty
	case utf8.RuneCountInString(text) > MaxNoteRunes:
		return ErrNoteTooLong
	case !slices.Contains(noteSources, source):
		return fmt.Errorf("%w: %q", ErrBadNoteSource, source)
	}
	return nil
}

func newNoteID() (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("making a note id: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func (s *FileStore) readNotes() ([]Note, error) {
	raw, err := os.ReadFile(s.notesPath())
	if errors.Is(err, fs.ErrNotExist) {
		return []Note{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading notes: %w", err)
	}
	var doc notesDocument
	if unmarshalErr := json.Unmarshal(raw, &doc); unmarshalErr != nil {
		return nil, fmt.Errorf("notes file is corrupt: %w", unmarshalErr)
	}
	return doc.Notes, nil
}

func (s *FileStore) writeNotes(notes []Note) error {
	raw, err := json.MarshalIndent(notesDocument{Notes: notes}, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding notes: %w", err)
	}
	if writeErr := s.writeFile(s.notesPath(), append(raw, '\n')); writeErr != nil {
		return fmt.Errorf("writing notes: %w", writeErr)
	}
	return nil
}
