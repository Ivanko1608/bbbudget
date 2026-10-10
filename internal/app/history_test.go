package app

import (
	"context"
	"errors"
	"testing"

	"bbbudget/internal/budget"
	"bbbudget/internal/money"
	"bbbudget/internal/store"
)

var (
	byAssistant = store.Origin{Source: store.SourceMCP}
	byBrowser   = store.Origin{Source: store.SourceWeb}
)

func setRent(amount string) []budget.Op {
	a := money.MustDecimal(amount)
	return []budget.Op{budget.NewOp("set_item", "rent", budget.ItemPatch{Amount: &a})}
}

// mustSetRent saves a new rent as origin, on top of whatever is current.
func mustSetRent(t *testing.T, a *App, origin store.Origin, amount string) {
	t.Helper()
	ctx := context.Background()
	if _, err := a.Apply(ctx, origin, "home", store.AnyRevision, setRent(amount)); err != nil {
		t.Fatal(err)
	}
}

func rent(t *testing.T, a *App) string {
	t.Helper()
	v, err := a.Get(context.Background(), "home")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range v.Budget.Items {
		if item.ID == "rent" {
			return item.Amount.String()
		}
	}
	t.Fatal("no rent item")
	return ""
}

func TestUndoRestoresTheVersionBefore(t *testing.T) {
	a := testApp(t)
	mustSetRent(t, a, byAssistant, "1200")

	if _, err := a.Undo(context.Background(), byAssistant, "home", UndoRequest{}); err != nil {
		t.Fatal(err)
	}

	if got := rent(t, a); got != "1150" {
		t.Errorf("rent after undo: %s", got)
	}
}

func TestUndoIsItselfAChangeThatCanBeUndone(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	mustSetRent(t, a, byAssistant, "1200")
	if _, err := a.Undo(ctx, byAssistant, "home", UndoRequest{}); err != nil {
		t.Fatal(err)
	}

	if _, err := a.Undo(ctx, byAssistant, "home", UndoRequest{}); err != nil {
		t.Fatal(err)
	}

	if got := rent(t, a); got != "1200" {
		t.Errorf("rent after undoing the undo: %s", got)
	}
}

func TestUndoIsRecordedUnderWhoAskedForIt(t *testing.T) {
	a := testApp(t)
	mustSetRent(t, a, byAssistant, "1200")

	undone, err := a.Undo(context.Background(), byBrowser, "home", UndoRequest{})
	if err != nil {
		t.Fatal(err)
	}

	entries, err := a.History(context.Background(), "home", 1)
	if err != nil {
		t.Fatal(err)
	}
	newest := entries[0]
	if newest.Revision != undone.Revision {
		t.Errorf("newest entry is r%d, the undo saved r%d", newest.Revision, undone.Revision)
	}
	if newest.Source != store.SourceWeb {
		t.Errorf("source: %s", newest.Source)
	}
	if newest.Undoes != 2 {
		t.Errorf("undoes: r%d", newest.Undoes)
	}
}

// The AI undoing "its" change must not silently throw away an edit the
// person made after it.
func TestUndoRefusesToDropLaterChanges(t *testing.T) {
	a := testApp(t)
	mustSetRent(t, a, byAssistant, "1200") // r2
	mustSetRent(t, a, byBrowser, "1250")   // r3

	_, err := a.Undo(context.Background(), byAssistant, "home", UndoRequest{Revision: 2})

	var conflict *UndoConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("want an UndoConflictError, got %v", err)
	}
	if later := conflict.Later; len(later) != 1 || later[0].Source != store.SourceWeb {
		t.Errorf("the refusal should name the browser change: %+v", later)
	}
	if got := rent(t, a); got != "1250" {
		t.Errorf("a refused undo changed the budget: rent %s", got)
	}
}

func TestForcedUndoDropsLaterChanges(t *testing.T) {
	a := testApp(t)
	mustSetRent(t, a, byAssistant, "1200") // r2
	mustSetRent(t, a, byBrowser, "1250")   // r3

	_, err := a.Undo(context.Background(), byAssistant, "home", UndoRequest{Revision: 2, Force: true})

	if err != nil {
		t.Fatal(err)
	}
	if got := rent(t, a); got != "1150" {
		t.Errorf("rent after a forced undo: %s", got)
	}
}

// A forced undo carries the newest revision the person was shown. A change
// that lands after that (the AI, another tab) makes it refuse again, so
// nothing is dropped that nobody saw.
func TestForcedUndoRefusesChangesNobodySaw(t *testing.T) {
	a := testApp(t)
	mustSetRent(t, a, byBrowser, "1200")   // r2
	mustSetRent(t, a, byBrowser, "1250")   // r3: the person was shown r1–r3
	mustSetRent(t, a, byAssistant, "1300") // r4 lands unseen
	shownUpToR3 := UndoRequest{Revision: 2, Force: true, Seen: 3}

	_, err := a.Undo(context.Background(), byBrowser, "home", shownUpToR3)

	var conflict *UndoConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("want an UndoConflictError, got %v", err)
	}
	if got := conflict.Later[0].Revision; got != 4 {
		t.Errorf("the refusal should start with the unseen r4, starts with r%d", got)
	}
	if got := rent(t, a); got != "1300" {
		t.Errorf("a refused undo changed the budget: rent %s", got)
	}
}

func TestUndoErrors(t *testing.T) {
	cases := []struct {
		name string
		id   string
		req  UndoRequest
		want error
	}{
		{"the creation", "home", UndoRequest{}, ErrNothingToUndo},
		{"a revision that never existed", "home", UndoRequest{Revision: 7}, ErrBadInput},
		{"an unknown budget", "nope", UndoRequest{}, store.ErrNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := testApp(t)

			_, err := a.Undo(context.Background(), byBrowser, c.id, c.req)

			if !errors.Is(err, c.want) {
				t.Errorf("got %v, want %v", err, c.want)
			}
		})
	}
}

func TestUndoPastTheHistoryLimit(t *testing.T) {
	a := testApp(t)
	a.Store.(*store.FileStore).HistoryLimit = 3
	for _, amount := range []string{"1200", "1300", "1400", "1500"} { // r2–r5: r1 and r2 age out
		mustSetRent(t, a, byBrowser, amount)
	}
	ctx := context.Background()

	_, oldest := a.Undo(ctx, byBrowser, "home", UndoRequest{Revision: 3, Force: true})
	_, next := a.Undo(ctx, byBrowser, "home", UndoRequest{Revision: 4, Force: true})

	if !errors.Is(oldest, ErrTooOld) {
		t.Errorf("undoing the oldest change kept: %v", oldest)
	}
	if next != nil {
		t.Errorf("undoing the next one: %v", next)
	}
}
