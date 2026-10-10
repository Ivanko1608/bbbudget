package store

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"bbbudget/internal/budget"
	"bbbudget/internal/money"
)

func setRent(amount string) func(budget.Budget) (budget.Budget, error) {
	return func(b budget.Budget) (budget.Budget, error) {
		b.Items[1].Amount = money.MustDecimal(amount)
		return b, nil
	}
}

func as(source string) context.Context { return WithChange(ctx, Change{Source: source}) }

func autosave() context.Context { return WithChange(ctx, Change{Source: "web", Autosave: true}) }

func mustCreate(t testing.TB, s *FileStore, c context.Context, b budget.Budget) {
	t.Helper()
	if _, err := s.Create(c, "home", b); err != nil {
		t.Fatal(err)
	}
}

func mustUpdate(t testing.TB, s *FileStore, c context.Context, fn func(budget.Budget) (budget.Budget, error)) {
	t.Helper()
	if _, err := s.Update(c, "home", AnyRevision, fn); err != nil {
		t.Fatal(err)
	}
}

// clock lets a test move time forward between writes.
type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

func TestHistoryRecordsEveryWrite(t *testing.T) {
	s, b := newStore(t)
	mustCreate(t, s, as("cli"), b)
	mustUpdate(t, s, as("mcp"), setRent("1200"))
	h, err := s.History(ctx, "home", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 2 || h[0].Revision != 2 || h[0].Source != "mcp" || h[1].Summary != "created" || h[1].Source != "cli" {
		t.Fatalf("history: %+v", h)
	}
	if !strings.Contains(h[0].Summary, "amount 1150 → 1200") {
		t.Errorf("summary: %q", h[0].Summary)
	}
	rent := budget.Difference{Kind: budget.Changed, Thing: "item", Name: "Rent", Field: "amount", Old: "1150", New: "1200"}
	if len(h[0].Changes) != 1 || h[0].Changes[0] != rent || h[1].Changes != nil {
		t.Errorf("changes: r2 %+v, r1 %+v", h[0].Changes, h[1].Changes)
	}
	if !h[0].Undoable || h[1].Undoable {
		t.Errorf("undoable: r2 %v, r1 (the creation) %v", h[0].Undoable, h[1].Undoable)
	}
	old, err := s.Snapshot(ctx, "home", 1)
	if err != nil || old.Items[1].Amount.String() != "1150" {
		t.Fatalf("snapshot r1: %v %v", old.Items[1].Amount, err)
	}
	if _, err := s.Snapshot(ctx, "home", 9); !errors.Is(err, ErrNoSnapshot) {
		t.Fatalf("missing revision: %v", err)
	}
}

func TestRejectedWriteLeavesNoHistory(t *testing.T) {
	s, b := newStore(t)
	mustCreate(t, s, ctx, b)
	if _, err := s.Update(ctx, "home", AnyRevision, func(b budget.Budget) (budget.Budget, error) {
		b.Items[0].Currency = "XXX" // invalid
		return b, nil
	}); err == nil {
		t.Fatal("invalid write accepted")
	}
	if h, _ := s.History(ctx, "home", 0); len(h) != 1 {
		t.Fatalf("history after a rejected write: %+v", h)
	}
}

func TestAutosavesCoalesce(t *testing.T) {
	s, b := newStore(t)
	c := &clock{time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
	s.now = c.now
	mustCreate(t, s, ctx, b)
	for _, amt := range []string{"1", "12", "120", "1200"} { // typing, autosaved
		c.add(time.Second)
		mustUpdate(t, s, autosave(), setRent(amt))
	}
	c.add(time.Second)
	mustUpdate(t, s, as("mcp"), setRent("1300")) // never merged with autosaves
	c.add(time.Second)
	mustUpdate(t, s, autosave(), setRent("1400"))
	c.add(coalesceWindow + time.Second)
	mustUpdate(t, s, autosave(), setRent("1500")) // too late to merge
	c.add(time.Second)
	mustUpdate(t, s, as("web"), setRent("1600")) // a deliberate action, not an autosave
	h, err := s.History(ctx, "home", 0)
	if err != nil {
		t.Fatal(err)
	}
	var revs []int64
	for _, e := range h {
		revs = append(revs, e.Revision)
	}
	if want := []int64{9, 8, 7, 6, 5, 1}; !slices.Equal(revs, want) {
		t.Fatalf("revisions %v, want %v", revs, want)
	}
	if !strings.Contains(h[4].Summary, "amount 1150 → 1200") { // r2–r5 as one change
		t.Errorf("coalesced summary: %q", h[4].Summary)
	}
	// Merged autosaves undo to the version before the run.
	if old, err := s.Snapshot(ctx, "home", 1); err != nil || old.Items[1].Amount.String() != "1150" {
		t.Fatalf("snapshot before the run: %v", err)
	}
}

func TestHistoryIsCapped(t *testing.T) {
	s, b := newStore(t)
	s.HistoryLimit = 5
	mustCreate(t, s, ctx, b)
	for i := range 9 {
		mustUpdate(t, s, as("cli"), setRent(strconv.Itoa(i+1)))
	}
	h, err := s.History(ctx, "home", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 5 || h[0].Revision != 10 || h[4].Revision != 6 {
		t.Fatalf("capped history: %+v", h)
	}
	if h[4].Undoable {
		t.Error("the oldest entry kept has no earlier version to go back to")
	}
	// The file is appended to, and rewritten only once it reaches twice the
	// limit, so it never holds more than 2×5 lines.
	if n := fileLines(t, s); n > 10 {
		t.Fatalf("history file has %d lines", n)
	}
}

// A budget saved before history existed gets its current state recorded as
// the baseline on its first tracked write, so that write can be undone.
func TestBaselineForBudgetsWithoutHistory(t *testing.T) {
	s, b := newStore(t)
	mustCreate(t, s, ctx, b)
	os.Remove(s.historyPath("home"))
	mustUpdate(t, s, as("mcp"), setRent("1200"))
	h, _ := s.History(ctx, "home", 0)
	if len(h) != 2 || h[1].Source != SourceEarlier {
		t.Fatalf("history: %+v", h)
	}
	if old, err := s.Snapshot(ctx, "home", 1); err != nil || old.Items[1].Amount.String() != "1150" {
		t.Fatalf("baseline snapshot: %v", err)
	}
}

// History is written before the budget. If the budget write then fails, the
// entry is newer than the budget; it must be ignored, and replaced by the
// next real write of that revision.
func TestPhantomEntryIsIgnored(t *testing.T) {
	s, b := newStore(t)
	mustCreate(t, s, ctx, b)
	cur, _ := s.Get(ctx, "home")
	next := cur
	next.Revision, next.Budget.Items[1].Amount = 2, money.MustDecimal("9999")
	if err := s.record(as("mcp"), &cur, next); err != nil { // the budget write "failed" after this
		t.Fatal(err)
	}
	if h, _ := s.History(ctx, "home", 0); len(h) != 1 {
		t.Fatalf("phantom listed: %+v", h)
	}
	mustUpdate(t, s, as("cli"), setRent("1200"))
	h, _ := s.History(ctx, "home", 0)
	if len(h) != 2 || h[0].Source != "cli" || !strings.Contains(h[0].Summary, "1150 → 1200") {
		t.Fatalf("after the real r2: %+v", h)
	}
	if snap, _ := s.Snapshot(ctx, "home", 2); snap.Items[1].Amount.String() != "1200" {
		t.Fatalf("snapshot r2 is the phantom: %v", snap.Items[1].Amount)
	}
}

func TestDeleteRemovesHistory(t *testing.T) {
	s, b := newStore(t)
	mustCreate(t, s, ctx, b)
	if err := s.Delete(ctx, "home"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.historyPath("home")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("history left behind: %v", err)
	}
	mustCreate(t, s, ctx, b) // a new budget under the same id starts fresh
	if h, _ := s.History(ctx, "home", 0); len(h) != 1 {
		t.Fatalf("history of the new budget: %+v", h)
	}
}

// A damaged history must never stop the budget from being saved: bad lines
// are skipped, and a line cut short by a crash doesn't swallow the next one.
func TestCorruptHistoryDoesNotBlockWrites(t *testing.T) {
	s, b := newStore(t)
	mustCreate(t, s, as("cli"), b)
	f, err := os.OpenFile(s.historyPath("home"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("this is not json\n{\"revision\": 2, \"budget\": {\"name\"") // garbage, then a half-written line
	f.Close()
	mustUpdate(t, s, as("mcp"), setRent("1200"))
	mustUpdate(t, s, as("mcp"), setRent("1300"))
	h, err := s.History(ctx, "home", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 3 || h[0].Revision != 3 || h[1].Revision != 2 || h[2].Revision != 1 {
		t.Fatalf("history around corrupt lines: %+v", h)
	}
	if old, err := s.Snapshot(ctx, "home", 2); err != nil || old.Items[1].Amount.String() != "1200" {
		t.Fatalf("snapshot r2: %v", err)
	}
}

func fileLines(t testing.TB, s *FileStore) int {
	t.Helper()
	raw, err := os.ReadFile(s.historyPath("home"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(raw), "\n")
}

// BenchmarkAutosave is a save with a full history behind it: the cost of
// each keystroke the editor saves.
func BenchmarkAutosave(bm *testing.B) {
	s, b := newStore(bm)
	mustCreate(bm, s, ctx, b)
	for i := range DefaultHistoryLimit {
		mustUpdate(bm, s, as("cli"), setRent(strconv.Itoa(1000+i)))
	}
	bm.ResetTimer()
	for i := range bm.N {
		mustUpdate(bm, s, as("cli"), setRent(strconv.Itoa(2000+i%1000)))
	}
}
