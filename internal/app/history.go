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

// ErrNothingToUndo means the change created the budget: there is no earlier
// version to go back to.
var ErrNothingToUndo = errors.New("nothing to undo: that change created the budget")

// ErrTooOld means the version before the change has aged out of the history.
var ErrTooOld = errors.New("can't undo that far back: the history no longer keeps the version before that change")

// LatestRevision in an UndoRequest means the most recent change.
const LatestRevision = 0

// UndoRequest says which change to undo and how far the caller agreed to go.
type UndoRequest struct {
	// Revision is the change to undo, or LatestRevision.
	Revision int64
	// Force also undoes every change made after it. Without it, Undo refuses
	// when there are any, and lists them.
	Force bool
	// Seen is the newest revision the caller was shown when it chose to
	// force (0 to skip the check). If more changes have landed since, Undo
	// refuses again with the up-to-date list, so a forced undo never drops a
	// change nobody was shown.
	Seen int64
}

// Undo reverts a change by saving the version from just before it as a new
// revision. History is never rewritten, so an undo can itself be undone.
// Going back to an old version throws away everything after it, so later
// changes are only undone with Force (see UndoRequest).
func (a *App) Undo(ctx context.Context, id string, req UndoRequest) (*View, error) {
	latest, err := a.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	revision := req.Revision
	if revision == LatestRevision {
		revision = latest.Revision
	}
	history, err := a.Store.History(ctx, id, store.AllEntries)
	if err != nil {
		return nil, err
	}
	target, later, previous, err := splitAt(history, revision)
	if err != nil {
		return nil, fmt.Errorf("%w: r%d is not a change in the history of %s", ErrBadInput, revision, id)
	}
	if !target.Undoable {
		if revision == 1 || target.Source == store.SourceEarlier {
			return nil, ErrNothingToUndo
		}
		return nil, ErrTooOld
	}
	unseen := req.Seen != 0 && req.Seen != latest.Revision
	if len(later) > 0 && (!req.Force || unseen) {
		return nil, &UndoConflictError{Revision: revision, Later: later}
	}

	old, err := a.Store.Snapshot(ctx, id, previous.Revision)
	if errors.Is(err, store.ErrNoSnapshot) { // trimmed since the listing above
		return nil, ErrTooOld
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
	c.Summary = fmt.Sprintf("undid %s (%s)", what, target.Summary)
	// Guarded by the revision read above, so a write in between is a
	// conflict instead of being lost.
	r, err := a.Store.Update(store.WithChange(ctx, c), id, latest.Revision, func(budget.Budget) (budget.Budget, error) { return old, nil })
	if err != nil {
		return nil, err
	}
	return view(r)
}

var errNotInHistory = errors.New("not in the history")

// splitAt finds revision in a newest-first history and returns that entry,
// the changes made after it (newest first) and the entry before it, which
// is the version an undo goes back to. previous is the zero Entry when
// target is the oldest entry kept.
func splitAt(history []store.Entry, revision int64) (target store.Entry, later []store.Entry, previous store.Entry, err error) {
	i := slices.IndexFunc(history, func(e store.Entry) bool { return e.Revision == revision })
	if i < 0 {
		return store.Entry{}, nil, store.Entry{}, errNotInHistory
	}
	if i+1 < len(history) {
		previous = history[i+1]
	}
	return history[i], history[:i], previous, nil
}

func sourceLabel(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
