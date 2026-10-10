package store

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"time"

	"bbbudget/internal/budget"
)

// Who made a change, as the history records it.
const (
	SourceWeb         = "web"          // the browser UI
	SourceMCP         = "mcp"          // an AI assistant over MCP
	SourceCLI         = "cli"          // the command line
	SourceAutoRefresh = "auto-refresh" // scheduled exchange-rate refreshes
	// SourceEarlier marks the baseline entry recorded for a budget that
	// existed before history was kept.
	SourceEarlier = "earlier"
)

// Change says who is making a write, for the history. Transports put one on
// the context (WithChange); the store reads it when it records the write.
type Change struct {
	// Source is who wrote: one of the Source constants.
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

// Entry is one write in a budget's history, as listings show it.
type Entry struct {
	// Revision is the revision this write created.
	Revision int64     `json:"revision"`
	At       time.Time `json:"at"`
	Source   string    `json:"source"`
	Summary  string    `json:"summary"`
	Autosave bool      `json:"autosave,omitempty"`
	// Undoable is false for the oldest entry kept: there is no earlier
	// version to go back to.
	Undoable bool `json:"undoable"`
}

// line is one line of the history file: an entry plus the whole budget as of
// its revision. The budget stays raw JSON until a snapshot actually needs it,
// so listing and recording never decode hundreds of documents.
type line struct {
	Revision int64     `json:"revision"`
	At       time.Time `json:"at"`
	Source   string    `json:"source"`
	Summary  string    `json:"summary"`
	Autosave bool      `json:"autosave,omitempty"`
	// Replaces is set when consecutive autosaves merge: this line takes the
	// place of the entry for that revision.
	Replaces int64           `json:"replaces,omitempty"`
	Budget   json.RawMessage `json:"budget"`
}

func (l line) entry() Entry {
	return Entry{Revision: l.Revision, At: l.At, Source: l.Source, Summary: l.Summary, Autosave: l.Autosave}
}

func (l line) budget() (budget.Budget, error) {
	var b budget.Budget
	err := json.Unmarshal(l.Budget, &b)
	return b, err
}

// ErrNoSnapshot means the history no longer (or never did) hold a revision.
var ErrNoSnapshot = errors.New("that revision is not in the history")

// DefaultHistoryLimit is how many entries a budget's history keeps.
const DefaultHistoryLimit = 200

// DefaultHistoryPage is how many entries a history listing shows when the
// caller doesn't say.
const DefaultHistoryPage = 20

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

// readHistory returns the history, oldest first, capped at the limit, and how
// many lines are in the file. It keeps only revisions up to current: an entry
// newer than the stored budget is left over from a write that failed after
// its history was saved, and the next line for that revision wins.
//
// Lines that don't parse (a crash halfway through a write, a hand edit) are
// skipped. The history is secondary to the budget and must never stop it
// from being saved.
func (s *FileStore) readHistory(id string, current int64) (out []line, lines int, err error) {
	f, err := os.Open(s.historyPath(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		lines++
		var l line
		if json.Unmarshal(sc.Bytes(), &l) != nil || l.Revision <= 0 || len(l.Budget) == 0 || l.Revision > current {
			continue
		}
		for len(out) > 0 && out[len(out)-1].Revision >= l.Revision {
			out = out[:len(out)-1]
		}
		if n := len(out); l.Replaces != 0 && n > 0 && out[n-1].Revision == l.Replaces {
			out = out[:n-1]
		}
		out = append(out, l)
	}
	if err := sc.Err(); err != nil {
		return nil, 0, err
	}
	if over := len(out) - s.limit(); over > 0 {
		out = out[over:]
	}
	return out, lines, nil
}

// record adds next to the history. It runs under the store lock and before
// the budget itself is written, so a failure here leaves the budget
// untouched. prev is nil when the budget is being created.
//
// A write normally appends one line. The file is rewritten only when it has
// grown to twice the limit (merged autosaves leave superseded lines behind),
// which keeps each save cheap however long the history is.
func (s *FileStore) record(ctx context.Context, prev *Record, next Record) error {
	raw, err := json.Marshal(next.Budget)
	if err != nil {
		return err
	}
	c := ChangeFrom(ctx)
	l := line{Revision: next.Revision, At: next.UpdatedAt, Source: c.Source, Summary: c.Summary, Autosave: c.Autosave, Budget: raw}
	if prev == nil {
		if l.Summary == "" {
			l.Summary = "created"
		}
		return s.rewriteHistory(next.ID, []line{l})
	}

	entries, lines, err := s.readHistory(next.ID, prev.Revision)
	if err != nil {
		return err
	}
	if len(entries) == 0 { // a budget from before history was kept
		old, err := json.Marshal(prev.Budget)
		if err != nil {
			return err
		}
		base := line{Revision: prev.Revision, At: prev.UpdatedAt, Source: SourceEarlier, Summary: "as it was before history was kept", Budget: old}
		if l.Summary == "" {
			l.Summary = budget.DescribeChange(prev.Budget, next.Budget)
		}
		return s.rewriteHistory(next.ID, []line{base, l})
	}

	// An autosave right after another autosave from the same source replaces
	// it, and is described from the version before that run of autosaves.
	before := prev.Budget
	if n := len(entries); c.Autosave && c.Summary == "" && n >= 2 {
		last := entries[n-1]
		if last.Autosave && last.Source == c.Source && last.Revision == prev.Revision && next.UpdatedAt.Sub(last.At) < coalesceWindow {
			if b, err := entries[n-2].budget(); err == nil {
				l.Replaces, before = last.Revision, b
				entries = entries[:n-1]
			}
		}
	}
	if l.Summary == "" {
		l.Summary = budget.DescribeChange(before, next.Budget)
	}
	if lines+1 > 2*s.limit() {
		if l.Replaces != 0 {
			l.Replaces = 0 // the replaced entry is already dropped from entries
		}
		return s.rewriteHistory(next.ID, append(entries, l))
	}
	return s.appendHistory(next.ID, l)
}

// appendHistory adds one line to the end of the history file and syncs it.
func (s *FileStore) appendHistory(id string, l line) error {
	data, err := json.Marshal(l)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.historyPath(id), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	// A crash halfway through an earlier append can leave a line without its
	// newline. Start on a fresh line so this entry isn't glued to it.
	if st, err := f.Stat(); err != nil {
		return err
	} else if st.Size() > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, st.Size()-1); err != nil {
			return err
		}
		if last[0] != '\n' {
			data = append([]byte{'\n'}, data...)
		}
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

// rewriteHistory replaces the history file with lines, atomically.
func (s *FileStore) rewriteHistory(id string, lines []line) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, l := range lines {
		if err := enc.Encode(l); err != nil {
			return err
		}
	}
	return s.writeFile(s.historyPath(id), buf.Bytes())
}

// History returns the newest entries first. limit <= 0 means all of them.
func (s *FileStore) History(ctx context.Context, id string, limit int) (out []Entry, err error) {
	err = s.withLock(false, func() error {
		cur, err := s.read(id)
		if err != nil {
			return err
		}
		lines, _, err := s.readHistory(id, cur.Revision)
		if err != nil {
			return err
		}
		for i := len(lines) - 1; i >= 0 && (limit <= 0 || len(out) < limit); i-- {
			e := lines[i].entry()
			e.Undoable = i > 0
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
		lines, _, err := s.readHistory(id, cur.Revision)
		if err != nil {
			return err
		}
		for _, l := range lines {
			if l.Revision == rev {
				b, err = l.budget()
				return err
			}
		}
		return ErrNoSnapshot
	})
	return b, err
}
