package store

import (
	"context"
	"errors"
	"os"
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

// clock lets a test move time forward between writes.
type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

func TestHistoryRecordsEveryWrite(t *testing.T) {
	s, b := newStore(t)
	s.Create(as("cli"), "home", b)
	if _, err := s.Update(as("mcp"), "home", AnyRevision, setRent("1200")); err != nil {
		t.Fatal(err)
	}
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
	if h[0].Budget != nil {
		t.Error("listing should not carry documents")
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
	s.Create(ctx, "home", b)
	s.Update(ctx, "home", AnyRevision, func(b budget.Budget) (budget.Budget, error) {
		b.Items[0].Currency = "XXX" // invalid
		return b, nil
	})
	if h, _ := s.History(ctx, "home", 0); len(h) != 1 {
		t.Fatalf("history after a rejected write: %+v", h)
	}
}

func TestAutosavesCoalesce(t *testing.T) {
	s, b := newStore(t)
	c := &clock{time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
	s.now = c.now
	s.Create(ctx, "home", b)
	for _, amt := range []string{"1", "12", "120", "1200"} { // typing, autosaved
		c.add(time.Second)
		s.Update(autosave(), "home", AnyRevision, setRent(amt))
	}
	c.add(time.Second)
	s.Update(as("mcp"), "home", AnyRevision, setRent("1300")) // never merged with autosaves
	c.add(time.Second)
	s.Update(autosave(), "home", AnyRevision, setRent("1400"))
	c.add(coalesceWindow + time.Second)
	s.Update(autosave(), "home", AnyRevision, setRent("1500")) // too late to merge
	c.add(time.Second)
	s.Update(as("web"), "home", AnyRevision, setRent("1600")) // a deliberate action, not an autosave
	h, _ := s.History(ctx, "home", 0)
	var revs []int64
	for _, e := range h {
		revs = append(revs, e.Revision)
	}
	if want := []int64{9, 8, 7, 6, 5, 1}; !equal(revs, want) {
		t.Fatalf("revisions %v, want %v", revs, want)
	}
	if !strings.Contains(h[4].Summary, "amount 1150 → 1200") { // r2–r5 as one change
		t.Errorf("coalesced summary: %q", h[3].Summary)
	}
}

func TestHistoryIsCapped(t *testing.T) {
	s, b := newStore(t)
	s.HistoryLimit = 5
	s.Create(ctx, "home", b)
	for i := range 9 {
		s.Update(as("cli"), "home", AnyRevision, setRent(string(rune('1'+i))))
	}
	h, _ := s.History(ctx, "home", 0)
	if len(h) != 5 || h[0].Revision != 10 || h[4].Revision != 6 {
		t.Fatalf("capped history: %+v", h)
	}
}

// A budget saved before history existed gets its current state recorded as
// the baseline on its first tracked write, so that write can be undone.
func TestBaselineForBudgetsWithoutHistory(t *testing.T) {
	s, b := newStore(t)
	s.Create(ctx, "home", b)
	os.Remove(s.historyPath("home"))
	s.Update(as("mcp"), "home", AnyRevision, setRent("1200"))
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
	s.Create(ctx, "home", b)
	cur, _ := s.Get(ctx, "home")
	next := cur
	next.Revision, next.Budget.Items[1].Amount = 2, money.MustDecimal("9999")
	if err := s.record(as("mcp"), &cur, next); err != nil { // the budget write "failed" after this
		t.Fatal(err)
	}
	if h, _ := s.History(ctx, "home", 0); len(h) != 1 {
		t.Fatalf("phantom listed: %+v", h)
	}
	s.Update(as("cli"), "home", AnyRevision, setRent("1200"))
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
	s.Create(ctx, "home", b)
	s.Delete(ctx, "home")
	if _, err := os.Stat(s.historyPath("home")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("history left behind: %v", err)
	}
	s.Create(ctx, "home", b) // a new budget under the same id starts fresh
	if h, _ := s.History(ctx, "home", 0); len(h) != 1 {
		t.Fatalf("history of the new budget: %+v", h)
	}
}

func equal(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
