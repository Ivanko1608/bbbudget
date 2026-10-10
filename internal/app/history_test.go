package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"bbbudget/internal/budget"
	"bbbudget/internal/money"
	"bbbudget/internal/store"
)

func rentOp(amount string) budget.Op {
	a := money.MustDecimal(amount)
	return budget.NewOp("set_item", "rent", budget.ItemPatch{Amount: &a})
}

func rent(t *testing.T, a *App) string {
	t.Helper()
	v, err := a.Get(context.Background(), "home")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range v.Budget.Items {
		if l.ID == "rent" {
			return l.Amount.String()
		}
	}
	t.Fatal("no rent item")
	return ""
}

func TestUndoLatestAndUndoTheUndo(t *testing.T) {
	a := testApp(t)
	mcp := store.WithChange(context.Background(), store.Change{Source: store.SourceMCP})
	if _, err := a.Apply(mcp, "home", store.AnyRevision, []budget.Op{rentOp("1200")}); err != nil {
		t.Fatal(err)
	}
	v, err := a.Undo(mcp, "home", UndoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := rent(t, a); got != "1150" {
		t.Fatalf("rent after undo: %s", got)
	}
	h, _ := a.History(context.Background(), "home", 1)
	if h[0].Revision != v.Revision || h[0].Source != "mcp" || !strings.HasPrefix(h[0].Summary, "undid r2 (") {
		t.Fatalf("undo entry: %+v", h[0])
	}
	if _, err := a.Undo(mcp, "home", UndoRequest{}); err != nil { // redo
		t.Fatal(err)
	}
	if got := rent(t, a); got != "1200" {
		t.Fatalf("rent after undoing the undo: %s", got)
	}
}

// The AI undoing "its" change must not silently throw away an edit the
// person made after it.
func TestUndoRefusesToDropLaterChanges(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	mcp := store.WithChange(ctx, store.Change{Source: store.SourceMCP})
	web := store.WithChange(ctx, store.Change{Source: store.SourceWeb})
	a.Apply(mcp, "home", store.AnyRevision, []budget.Op{rentOp("1200")}) // r2
	a.Apply(web, "home", store.AnyRevision, []budget.Op{rentOp("1250")}) // r3
	_, err := a.Undo(mcp, "home", UndoRequest{Revision: 2})
	var conflict *UndoConflictError
	if !errors.As(err, &conflict) || len(conflict.Later) != 1 || conflict.Later[0].Source != "web" {
		t.Fatalf("want a conflict naming the web change, got %v", err)
	}
	if got := rent(t, a); got != "1250" {
		t.Fatalf("a refused undo changed the budget: %s", got)
	}
	if _, err := a.Undo(mcp, "home", UndoRequest{Revision: 2, Force: true}); err != nil {
		t.Fatal(err)
	}
	if got := rent(t, a); got != "1150" {
		t.Fatalf("forced undo: %s", got)
	}
}

func TestUndoEdgeCases(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	if _, err := a.Undo(ctx, "home", UndoRequest{}); !errors.Is(err, ErrNothingToUndo) {
		t.Fatalf("undoing the creation: %v", err)
	}
	if _, err := a.Undo(ctx, "home", UndoRequest{Revision: 7}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("unknown revision: %v", err)
	}
	if _, err := a.Undo(ctx, "nope", UndoRequest{}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown budget: %v", err)
	}
}

// A forced undo carries the newest revision the person was shown. A change
// that lands after that (the AI, another tab) makes it refuse again, so
// nothing is dropped that nobody saw.
func TestForcedUndoRefusesChangesNobodySaw(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	web := store.WithChange(ctx, store.Change{Source: store.SourceWeb})
	mcp := store.WithChange(ctx, store.Change{Source: store.SourceMCP})
	a.Apply(web, "home", store.AnyRevision, []budget.Op{rentOp("1200")}) // r2
	a.Apply(web, "home", store.AnyRevision, []budget.Op{rentOp("1250")}) // r3: the person sees r1–r3
	a.Apply(mcp, "home", store.AnyRevision, []budget.Op{rentOp("1300")}) // r4 lands unseen
	_, err := a.Undo(web, "home", UndoRequest{Revision: 2, Force: true, Seen: 3})
	var conflict *UndoConflictError
	if !errors.As(err, &conflict) || len(conflict.Later) != 2 || conflict.Later[0].Revision != 4 {
		t.Fatalf("want a fresh refusal listing r4 and r3, got %v", err)
	}
	if got := rent(t, a); got != "1300" {
		t.Fatalf("a refused undo changed the budget: %s", got)
	}
	if _, err := a.Undo(web, "home", UndoRequest{Revision: 2, Force: true, Seen: 4}); err != nil { // after seeing r4
		t.Fatal(err)
	}
	if got := rent(t, a); got != "1150" {
		t.Fatalf("forced undo: %s", got)
	}
}

func TestUndoPastTheHistoryLimit(t *testing.T) {
	a := testApp(t)
	a.Store.(*store.FileStore).HistoryLimit = 3
	ctx := context.Background()
	for _, amt := range []string{"1200", "1300", "1400", "1500"} { // r2–r5; r1, r2 age out
		a.Apply(ctx, "home", store.AnyRevision, []budget.Op{rentOp(amt)})
	}
	if _, err := a.Undo(ctx, "home", UndoRequest{Revision: 3, Force: true}); !errors.Is(err, ErrTooOld) {
		t.Fatalf("undoing the oldest kept change: %v", err)
	}
	if _, err := a.Undo(ctx, "home", UndoRequest{Revision: 4, Force: true}); err != nil {
		t.Fatalf("undoing the next one: %v", err)
	}
}
