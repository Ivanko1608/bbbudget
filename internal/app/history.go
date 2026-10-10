package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"bbbudget/internal/budget"
	"bbbudget/internal/store"
)

// History returns a budget's recent changes, newest first. It only passes
// the call through to the store, but the doors (HTTP, MCP, CLI) talk to App,
// never to the store directly, so this is where they get it.
func (a *App) History(ctx context.Context, id string, limit int) ([]store.Entry, error) {
	return a.Store.History(ctx, id, limit)
}

// UndoConflictError means undoing a change would also undo the changes made
// after it, and the caller didn't say that's fine.
type UndoConflictError struct {
	Revision int64
	Later    []store.Entry // newest first
}

func (e *UndoConflictError) Error() string {
	parts := make([]string, len(e.Later))
	for i, l := range e.Later {
		parts[i] = fmt.Sprintf("r%d (%s): %s", l.Revision, sourceLabel(l.Source), l.Summary)
	}
	return fmt.Sprintf("undoing r%d would also undo %d later change(s): %s. Undo those first, or force it to undo them all",
		e.Revision, len(e.Later), strings.Join(parts, "; "))
}

// ErrNothingToUndo means the change is the budget's first entry: there is no
// earlier version to go back to.
var ErrNothingToUndo = errors.New("nothing to undo: that is the oldest version in the history")

// Undo reverts the change that created a revision (0 means the latest) by
// saving the version from just before it as a new revision. History is never
// rewritten, so an undo can itself be undone.
//
// If changes were made after the one being undone, it refuses unless force
// is set, in which case those are undone too: going back to an old version
// throws away everything after it.
func (a *App) Undo(ctx context.Context, id string, revision int64, force bool) (*View, error) {
	latest, err := a.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if revision == 0 {
		revision = latest.Revision
	}
	history, err := a.Store.History(ctx, id, 0) // newest first
	if err != nil {
		return nil, err
	}

	// Find the change to undo. Entries before it in the list are later
	// changes; the entry after it is the version to go back to.
	i := slices.IndexFunc(history, func(e store.Entry) bool { return e.Revision == revision })
	if i < 0 {
		return nil, fmt.Errorf("%w: r%d is not a change in the history of %s", ErrBadInput, revision, id)
	}
	if i == len(history)-1 {
		return nil, ErrNothingToUndo
	}
	undone, later, previous := history[i], history[:i], history[i+1]
	if len(later) > 0 && !force {
		return nil, &UndoConflictError{Revision: revision, Later: later}
	}

	old, err := a.Store.Snapshot(ctx, id, previous.Revision)
	if errors.Is(err, store.ErrNoSnapshot) {
		return nil, fmt.Errorf("%w: the history no longer holds r%d", ErrNothingToUndo, previous.Revision)
	}
	if err != nil {
		return nil, err
	}

	// Record the undo itself, under whoever asked for it.
	what := fmt.Sprintf("r%d", revision)
	if len(later) > 0 {
		what = fmt.Sprintf("r%d–r%d", revision, latest.Revision)
	}
	c := store.ChangeFrom(ctx)
	c.Summary = fmt.Sprintf("undid %s (%s)", what, undone.Summary)
	// Guarded by the revision read above, so a write in between is a
	// conflict instead of being lost.
	r, err := a.Store.Update(store.WithChange(ctx, c), id, latest.Revision, func(budget.Budget) (budget.Budget, error) { return old, nil })
	if err != nil {
		return nil, err
	}
	return view(r)
}

func sourceLabel(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
