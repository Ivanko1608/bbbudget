package store

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"bbbudget/internal/budget"
)

// Change says who is making a write, for the history. Transports put one on
// the context (WithChange); the store reads it when it records the write.
type Change struct {
	// Source is who wrote: "web", "mcp", "cli", "auto-refresh", "assistant".
	Source string
	// Summary overrides the summary computed from the diff ("undid r12: …").
	Summary string
	// Autosave marks an editor's save-as-you-type write. Consecutive
	// autosaves from one source merge into one history entry.
	Autosave bool
}

type changeKey struct{}

// WithChange returns ctx carrying c.
func WithChange(ctx context.Context, c Change) context.Context {
	return context.WithValue(ctx, changeKey{}, c)
}

// ChangeFrom returns the Change on ctx, if any.
func ChangeFrom(ctx context.Context) Change {
	c, _ := ctx.Value(changeKey{}).(Change)
	return c
}

// Entry is one write in a budget's history.
type Entry struct {
	// Revision is the revision this write created.
	Revision int64     `json:"revision"`
	At       time.Time `json:"at"`
	Source   string    `json:"source"`
	Summary  string    `json:"summary"`
	Autosave bool      `json:"autosave,omitempty"`
	// Budget is the document as of Revision. History listings leave it out.
	Budget *budget.Budget `json:"budget,omitempty"`
}

// ErrNoSnapshot means the history no longer (or never did) hold a revision.
var ErrNoSnapshot = errors.New("that revision is not in the history")

// SourceEarlier marks the baseline entry recorded for a budget that existed
// before history was kept.
const SourceEarlier = "earlier"

// DefaultHistoryLimit is how many entries a budget's history keeps.
const DefaultHistoryLimit = 200

// coalesceWindow merges consecutive autosaves: the UI saves while you type,
// and one edit shouldn't fill the history with a dozen entries.
const coalesceWindow = 2 * time.Minute

func (s *FileStore) historyPath(id string) string { return s.path(id) + "l" } // home.json → home.jsonl

func (s *FileStore) limit() int {
	if s.HistoryLimit > 0 {
		return s.HistoryLimit
	}
	return DefaultHistoryLimit
}

// readHistory returns every entry on disk, oldest first, keeping only
// revisions up to current. A later write of the same revision wins: an entry
// newer than the stored budget is left over from a write that failed after
// its history was saved.
func (s *FileStore) readHistory(id string, current int64) ([]Entry, error) {
	f, err := os.Open(s.historyPath(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("history of %s is corrupt: %w", id, err)
		}
		if e.Revision > current {
			continue
		}
		for len(out) > 0 && out[len(out)-1].Revision >= e.Revision {
			out = out[:len(out)-1]
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// record adds next to the history. It runs under the store lock and before
// the budget itself is written, so a failure here leaves the budget
// untouched. prev is nil when the budget is being created.
func (s *FileStore) record(ctx context.Context, prev *Record, next Record) error {
	var entries []Entry
	if prev != nil {
		var err error
		if entries, err = s.readHistory(next.ID, prev.Revision); err != nil {
			return err
		}
		if len(entries) == 0 { // a budget from before history was kept
			b := prev.Budget
			entries = []Entry{{Revision: prev.Revision, At: prev.UpdatedAt, Source: SourceEarlier, Summary: "as it was before history was kept", Budget: &b}}
		}
	}
	c := ChangeFrom(ctx)
	e := Entry{Revision: next.Revision, At: next.UpdatedAt, Source: c.Source, Summary: c.Summary, Autosave: c.Autosave, Budget: &next.Budget}
	if n := len(entries); c.Autosave && c.Summary == "" && n >= 2 {
		last := entries[n-1]
		if last.Autosave && last.Source == c.Source && last.Revision == next.Revision-1 && next.UpdatedAt.Sub(last.At) < coalesceWindow {
			entries = entries[:n-1] // folded into this entry, described from the one before
		}
	}
	if e.Summary == "" {
		if n := len(entries); n > 0 && entries[n-1].Budget != nil {
			e.Summary = budget.DescribeChange(*entries[n-1].Budget, next.Budget)
		} else {
			e.Summary = "created"
		}
	}
	entries = append(entries, e)
	if over := len(entries) - s.limit(); over > 0 {
		entries = entries[over:]
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return s.writeFile(s.historyPath(next.ID), buf.Bytes())
}

// History returns the newest entries first, without documents. limit <= 0
// means all of them.
func (s *FileStore) History(ctx context.Context, id string, limit int) (out []Entry, err error) {
	err = s.withLock(false, func() error {
		cur, err := s.read(id)
		if err != nil {
			return err
		}
		entries, err := s.readHistory(id, cur.Revision)
		if err != nil {
			return err
		}
		for i := len(entries) - 1; i >= 0 && (limit <= 0 || len(out) < limit); i-- {
			e := entries[i]
			e.Budget = nil
			out = append(out, e)
		}
		return nil
	})
	return out, err
}

// Snapshot returns the budget as it was at revision rev.
func (s *FileStore) Snapshot(ctx context.Context, id string, rev int64) (b budget.Budget, err error) {
	err = s.withLock(false, func() error {
		cur, err := s.read(id)
		if err != nil {
			return err
		}
		if rev == cur.Revision {
			b = cur.Budget
			return nil
		}
		entries, err := s.readHistory(id, cur.Revision)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.Revision == rev && e.Budget != nil {
				b = *e.Budget
				return nil
			}
		}
		return ErrNoSnapshot
	})
	return b, err
}
