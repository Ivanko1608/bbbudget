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
	mcp := store.WithChange(context.Background(), store.Change{Source: "mcp"})
	if _, err := a.Apply(mcp, "home", store.AnyRevision, []budget.Op{rentOp("1200")}); err != nil {
		t.Fatal(err)
	}
	v, err := a.Undo(mcp, "home", 0, false)
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
	if _, err := a.Undo(mcp, "home", 0, false); err != nil { // redo
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
	mcp := store.WithChange(ctx, store.Change{Source: "mcp"})
	web := store.WithChange(ctx, store.Change{Source: "web"})
	a.Apply(mcp, "home", store.AnyRevision, []budget.Op{rentOp("1200")}) // r2
	a.Apply(web, "home", store.AnyRevision, []budget.Op{rentOp("1250")}) // r3
	_, err := a.Undo(mcp, "home", 2, false)
	var ue *UndoConflictError
	if !errors.As(err, &ue) || len(ue.Later) != 1 || ue.Later[0].Source != "web" {
		t.Fatalf("want a conflict naming the web change, got %v", err)
	}
	if got := rent(t, a); got != "1250" {
		t.Fatalf("a refused undo changed the budget: %s", got)
	}
	if _, err := a.Undo(mcp, "home", 2, true); err != nil {
		t.Fatal(err)
	}
	if got := rent(t, a); got != "1150" {
		t.Fatalf("forced undo: %s", got)
	}
}

func TestUndoEdgeCases(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	if _, err := a.Undo(ctx, "home", 0, false); !errors.Is(err, ErrNothingToUndo) {
		t.Fatalf("undoing the creation: %v", err)
	}
	if _, err := a.Undo(ctx, "home", 7, false); !errors.Is(err, ErrBadInput) {
		t.Fatalf("unknown revision: %v", err)
	}
	if _, err := a.Undo(ctx, "nope", 0, false); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown budget: %v", err)
	}
}
