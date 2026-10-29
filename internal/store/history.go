package store

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"bbbudget/internal/budget"
)

// Source is who made a write.
type Source string

const (
	SourceWeb         Source = "web"          // the browser UI
	SourceMCP         Source = "mcp"          // an AI assistant over MCP
	SourceAssistant   Source = "assistant"    // the assistant inside the app
	SourceCLI         Source = "cli"          // the command line
	SourceAutoRefresh Source = "auto-refresh" // scheduled exchange-rate refreshes
	SourceEarlier     Source = "earlier"      // the state of a budget saved before history was kept
)

// Origin says where a write comes from, for the history.
type Origin struct {
	Source Source
	// Autosave marks an editor's save-as-you-type write. Consecutive
	// autosaves from one source merge into one history entry.
	Autosave bool
	// Undoes is the revision this write undoes, when it is an undo.
	Undoes int64
}

// Entry is one write in a budget's history, as listings show it.
type Entry struct {
	// Revision is the revision this write created.
	Revision int64     `json:"revision"`
	At       time.Time `json:"at"`
	Source   Source    `json:"source"`
	// Summary is what changed, in English. Changes is the same, one
	// difference at a time; it is empty for the entry that created the budget.
	Summary  string              `json:"summary"`
	Changes  []budget.Difference `json:"changes,omitempty"`
	Undoes   int64               `json:"undoes,omitempty"`
	Autosave bool                `json:"autosave,omitempty"`
	// Undoable is false for the oldest entry kept: there is no earlier
	// version to go back to.
	Undoable bool `json:"undoable"`
}

// line is one line of the history file: an entry and the whole budget as of
// its revision. The budget stays raw JSON until a snapshot needs it, so
// listing and recording never decode hundreds of documents.
type line struct {
	Revision int64               `json:"revision"`
	At       time.Time           `json:"at"`
	Source   Source              `json:"source"`
	Summary  string              `json:"summary"`
	Changes  []budget.Difference `json:"changes,omitempty"`
	Undoes   int64               `json:"undoes,omitempty"`
	Autosave bool                `json:"autosave,omitempty"`
	// Replaces is the revision of the entry this line takes the place of,
	// when consecutive autosaves merge.
	Replaces int64           `json:"replaces,omitempty"`
	Budget   json.RawMessage `json:"budget"`
}

func (l line) entry() Entry {
	return Entry{
		Revision: l.Revision,
		At:       l.At,
		Source:   l.Source,
		Summary:  l.Summary,
		Changes:  l.Changes,
		Undoes:   l.Undoes,
		Autosave: l.Autosave,
	}
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

// AllEntries asks History for every entry it keeps.
const AllEntries = 0

// coalesceWindow is how close together autosaves must be to merge.
const coalesceWindow = 2 * time.Minute

func (s *FileStore) historyPath(id string) string { return filepath.Join(s.dir, id+".jsonl") }

func (s *FileStore) limit() int {
	if s.HistoryLimit > 0 {
		return s.HistoryLimit
	}
	return DefaultHistoryLimit
}

// History returns the newest entries first, at most limit of them
// (AllEntries for all).
func (s *FileStore) History(ctx context.Context, id string, limit int) ([]Entry, error) {
	var entries []Entry
	err := s.withLock(false, func() error {
		current, err := s.read(id)
		if err != nil {
			return err
		}
		file, err := s.readHistory(id, current.Revision)
		if err != nil {
			return err
		}
		entries = newestFirst(file.entries, limit)
		return nil
	})
	return entries, err
}

func newestFirst(lines []line, limit int) []Entry {
	n := len(lines)
	if limit > 0 {
		n = min(n, limit)
	}
	entries := make([]Entry, 0, n)
	for i := len(lines) - 1; len(entries) < n; i-- {
		e := lines[i].entry()
		e.Undoable = i > 0
		entries = append(entries, e)
	}
	return entries
}

// Snapshot returns the budget as it was at revision rev.
func (s *FileStore) Snapshot(ctx context.Context, id string, rev int64) (budget.Budget, error) {
	var snapshot budget.Budget
	err := s.withLock(false, func() error {
		current, err := s.read(id)
		if err != nil {
			return err
		}
		if rev == current.Revision {
			snapshot = current.Budget
			return nil
		}
		file, err := s.readHistory(id, current.Revision)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(file.entries, func(l line) bool { return l.Revision == rev })
		if i < 0 {
			return ErrNoSnapshot
		}
		snapshot, err = file.entries[i].budget()
		return err
	})
	return snapshot, err
}

// historyFile is a budget's history as read from disk.
type historyFile struct {
	entries []line // oldest first, at most the limit
	lines   int    // lines in the file, superseded and unreadable ones included
}

// readHistory reads the history of a budget whose current revision is
// current. Unreadable lines are skipped: the history is secondary to the
// budget, and a damaged file must never stop the budget from being saved.
func (s *FileStore) readHistory(id string, current int64) (historyFile, error) {
	raw, err := readLines(s.historyPath(id))
	if err != nil {
		return historyFile{}, err
	}
	var entries []line
	for _, data := range raw {
		l, ok := parseLine(data)
		if ok && l.Revision <= current {
			entries = apply(entries, l)
		}
	}
	return historyFile{entries: keepNewest(entries, s.limit()), lines: len(raw)}, nil
}

// readLines returns the non-empty lines of a file; none if it doesn't exist.
func readLines(path string) ([][]byte, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var lines [][]byte
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 16<<20)
	for scanner.Scan() {
		if len(bytes.TrimSpace(scanner.Bytes())) > 0 {
			lines = append(lines, bytes.Clone(scanner.Bytes()))
		}
	}
	return lines, scanner.Err()
}

func parseLine(data []byte) (line, bool) {
	var l line
	if err := json.Unmarshal(data, &l); err != nil {
		return line{}, false
	}
	return l, l.Revision > 0 && len(l.Budget) > 0
}

// apply adds l to entries the way a reader replays the file. A line for a
// revision takes the place of any entry for that revision or a later one:
// those were left by a write that failed after its history was saved.
func apply(entries []line, l line) []line {
	for len(entries) > 0 && entries[len(entries)-1].Revision >= l.Revision {
		entries = entries[:len(entries)-1]
	}
	if replacesLast(entries, l) {
		entries = entries[:len(entries)-1]
	}
	return append(entries, l)
}

func replacesLast(entries []line, l line) bool {
	if len(entries) == 0 || l.Replaces == 0 {
		return false
	}
	last := entries[len(entries)-1]
	return last.Revision == l.Replaces
}

func keepNewest(entries []line, limit int) []line {
	if over := len(entries) - limit; over > 0 {
		return entries[over:]
	}
	return entries
}

// record adds next to the history. prev is nil when the budget is being
// created. It runs under the store lock and before the budget itself is
// written, so a failure here leaves the budget untouched.
func (s *FileStore) record(origin Origin, prev *Record, next Record) error {
	nextVersion, err := newVersion(next)
	if err != nil {
		return err
	}
	if prev == nil {
		return s.startHistory(next.ID, createdLine(origin, nextVersion))
	}
	file, err := s.readHistory(prev.ID, prev.Revision)
	if err != nil {
		return err
	}
	if len(file.entries) == 0 {
		return s.startHistoryAfter(origin, *prev, nextVersion)
	}
	l, err := file.nextLine(origin, *prev, nextVersion)
	if err != nil {
		return err
	}
	return s.addLine(next.ID, file, l)
}

// nextLine is the line for next. An autosave that continues a run of
// autosaves takes the place of the run's last entry, and is described from
// the version before the run, so the run reads as one change.
func (h historyFile) nextLine(origin Origin, prev Record, next version) (line, error) {
	if !h.continuesAutosave(origin, prev, next.Record) {
		return changeLine(origin, prev.Budget, next), nil
	}
	n := len(h.entries)
	runEnd, beforeRun := h.entries[n-1], h.entries[n-2]
	before, err := beforeRun.budget()
	if err != nil {
		return line{}, err
	}
	l := changeLine(origin, before, next)
	l.Replaces = runEnd.Revision
	return l, nil
}

func (h historyFile) continuesAutosave(origin Origin, prev, next Record) bool {
	if !origin.Autosave || len(h.entries) < 2 {
		return false
	}
	last := h.entries[len(h.entries)-1]
	sameEditor := last.Autosave && last.Source == origin.Source
	sameRun := sameEditor && last.Revision == prev.Revision
	return sameRun && next.UpdatedAt.Sub(last.At) < coalesceWindow
}

// addLine appends l. Once the file holds twice the limit, it is rewritten
// instead, which drops superseded lines and entries past the limit.
func (s *FileStore) addLine(id string, h historyFile, l line) error {
	if h.lines < 2*s.limit() {
		return s.appendHistory(id, l)
	}
	return s.rewriteHistory(id, keepNewest(apply(h.entries, l), s.limit()))
}

func (s *FileStore) startHistory(id string, lines ...line) error {
	return s.rewriteHistory(id, lines)
}

// startHistoryAfter records next for a budget saved before history was kept.
// The budget as it was goes in first, so this write can be undone.
func (s *FileStore) startHistoryAfter(origin Origin, prev Record, next version) error {
	prevVersion, err := newVersion(prev)
	if err != nil {
		return err
	}
	beforeHistory := Origin{Source: SourceEarlier}
	baseline := newLine(beforeHistory, prevVersion, "as it was before history was kept")
	return s.startHistory(next.ID, baseline, changeLine(origin, prev.Budget, next))
}

// version is a stored budget with its document as JSON, for a history line.
type version struct {
	Record
	raw json.RawMessage
}

func newVersion(r Record) (version, error) {
	raw, err := json.Marshal(r.Budget)
	return version{Record: r, raw: raw}, err
}

func createdLine(origin Origin, v version) line {
	return newLine(origin, v, "created")
}

func changeLine(origin Origin, before budget.Budget, next version) line {
	diffs := budget.Differences(before, next.Budget)
	summary := budget.Describe(diffs)
	if origin.Undoes != 0 {
		summary = fmt.Sprintf("undid r%d: %s", origin.Undoes, summary)
	}
	l := newLine(origin, next, summary)
	l.Changes = diffs
	return l
}

func newLine(origin Origin, v version, summary string) line {
	return line{
		Revision: v.Revision,
		At:       v.UpdatedAt,
		Source:   origin.Source,
		Summary:  summary,
		Undoes:   origin.Undoes,
		Autosave: origin.Autosave,
		Budget:   v.raw,
	}
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
	if err = writeLine(f, data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// writeLine appends data and a newline to f and syncs it.
func writeLine(f *os.File, data []byte) error {
	ended, err := endsWithNewline(f)
	if err != nil {
		return err
	}
	if !ended {
		// A crash halfway through an earlier append left a line without its
		// newline. Start on a fresh line so this entry isn't glued to it.
		data = append([]byte{'\n'}, data...)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

// endsWithNewline reports whether f is empty or ends with a newline.
func endsWithNewline(f *os.File) (bool, error) {
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return true, err
	}
	last := make([]byte, 1)
	if _, err := f.ReadAt(last, info.Size()-1); err != nil && err != io.EOF {
		return false, err
	}
	return last[0] == '\n', nil
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
