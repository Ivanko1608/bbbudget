package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"bbbudget/internal/budget"
)

// FileStore keeps one JSON file per budget in a directory. Writes are atomic
// (temp file + fsync + rename) and serialised across processes with a lock
// file, so `bbbudget serve` and `bbbudget mcp` can share a directory.
type FileStore struct {
	dir string
	mu  sync.Mutex // serialises this process; the file lock serialises processes
	now func() time.Time
	// HistoryLimit is how many history entries each budget keeps
	// (DefaultHistoryLimit when zero).
	HistoryLimit int
}

// NewFileStore opens (and creates if needed) a store directory.
func NewFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &FileStore{dir: dir, now: time.Now}, nil
}

func (s *FileStore) path(id string) string { return filepath.Join(s.dir, id+".json") }

func (s *FileStore) withLock(exclusive bool, fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := lockFile(filepath.Join(s.dir, ".lock"), exclusive)
	if err != nil {
		return fmt.Errorf("lock store: %w", err)
	}
	defer unlock()
	return fn()
}

func (s *FileStore) read(id string) (Record, error) {
	if !ValidID(id) {
		return Record{}, ErrBadID
	}
	raw, err := os.ReadFile(s.path(id))
	if errors.Is(err, fs.ErrNotExist) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	var r Record
	if err := json.Unmarshal(raw, &r); err != nil {
		return Record{}, fmt.Errorf("budget %s is corrupt: %w", id, err)
	}
	r.ID = id
	return r, nil
}

// write saves a record. Callers validate it first (budget.Compile), before
// recording its history, so an invalid write leaves no trace.
func (s *FileStore) write(r Record) error {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return s.writeFile(s.path(r.ID), append(raw, '\n'))
}

// writeFile replaces path atomically: temp file, fsync, rename, fsync dir.
func (s *FileStore) writeFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(s.dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return syncDir(s.dir)
}

func (s *FileStore) List(ctx context.Context) ([]Meta, error) {
	var out []Meta
	err := s.withLock(false, func() error {
		entries, err := os.ReadDir(s.dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			id, ok := strings.CutSuffix(e.Name(), ".json")
			if !ok || !ValidID(id) {
				continue
			}
			r, err := s.read(id)
			if err != nil {
				return err
			}
			out = append(out, Meta{ID: id, Name: r.Budget.Name, Revision: r.Revision, UpdatedAt: r.UpdatedAt})
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, err
}

func (s *FileStore) Get(ctx context.Context, id string) (r Record, err error) {
	err = s.withLock(false, func() error {
		r, err = s.read(id)
		return err
	})
	return r, err
}

func (s *FileStore) Create(ctx context.Context, id string, b budget.Budget) (r Record, err error) {
	if !ValidID(id) {
		return Record{}, ErrBadID
	}
	err = s.withLock(true, func() error {
		if _, err := s.read(id); err == nil {
			return ErrExists
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		r = Record{ID: id, Revision: 1, UpdatedAt: s.now().UTC(), Budget: b}
		if _, err := budget.Compile(b); err != nil {
			return err
		}
		if err := s.record(ctx, nil, r); err != nil {
			return err
		}
		return s.write(r)
	})
	return r, err
}

func (s *FileStore) Update(ctx context.Context, id string, expected int64, fn func(budget.Budget) (budget.Budget, error)) (r Record, err error) {
	err = s.withLock(true, func() error {
		cur, err := s.read(id)
		if err != nil {
			return err
		}
		if expected != AnyRevision && expected != cur.Revision {
			return &ConflictError{Expected: expected, Actual: cur.Revision}
		}
		work, err := budget.Clone(cur.Budget) // fn may edit in place; cur stays the "before"
		if err != nil {
			return err
		}
		nb, err := fn(work)
		if err != nil {
			return err
		}
		r = Record{ID: id, Revision: cur.Revision + 1, UpdatedAt: s.now().UTC(), Budget: nb}
		if _, err := budget.Compile(nb); err != nil {
			return err // before recording: an invalid write leaves no trace
		}
		if err := s.record(ctx, &cur, r); err != nil {
			return err
		}
		return s.write(r)
	})
	return r, err
}

func (s *FileStore) Delete(ctx context.Context, id string) error {
	if !ValidID(id) {
		return ErrBadID
	}
	return s.withLock(true, func() error {
		err := os.Remove(s.path(id))
		if errors.Is(err, fs.ErrNotExist) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := os.Remove(s.historyPath(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	})
}

// DefaultDir is $BBBUDGET_DIR, else $XDG_DATA_HOME/bbbudget, else ~/.local/share/bbbudget.
func DefaultDir() string {
	if d := os.Getenv("BBBUDGET_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "bbbudget")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "bbbudget-data"
	}
	return filepath.Join(home, ".local", "share", "bbbudget")
}
