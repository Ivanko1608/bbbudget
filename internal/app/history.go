package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"bbbudget/internal/budget"
	"bbbudget/internal/i18n"
	"bbbudget/internal/store"
)

// History returns a budget's recent changes, newest first, with summaries
// in the caller's language.
func (a *App) History(ctx context.Context, id string, limit int) ([]store.Entry, error) {
	h, err := a.Store.History(ctx, id, limit)
	if err != nil {
		return nil, err
	}
	if l := a.Localizer(ctx); l.Locale() != i18n.Default {
		for i, e := range h {
			if e.Msg != nil {
				h[i].Summary = l.T(*e.Msg)
			}
		}
	}
	return h, nil
}

// UndoConflictError means undoing a change would also undo the changes made
// after it, and the caller didn't say that's fine.
type UndoConflictError struct {
	Change int64
	Later  []store.Entry // newest first
}

func (e *UndoConflictError) Error() string {
	parts := make([]string, len(e.Later))
	for i, l := range e.Later {
		parts[i] = fmt.Sprintf("r%d (%s): %s", l.Revision, sourceLabel(l.Source), l.Summary)
	}
	return fmt.Sprintf("undoing r%d would also undo %d later change(s): %s. Undo those first, or force it to undo them all",
		e.Change, len(e.Later), strings.Join(parts, "; "))
}

// ErrNothingToUndo means the change is the budget's first entry: there is no
// earlier version to go back to.
var ErrNothingToUndo = errors.New("nothing to undo: that is the oldest version in the history")

// Undo reverts the change that created revision change (0 = the latest) by
// saving the version from before it as a new revision. History is never
// rewritten, so an undo can itself be undone. If later changes exist it
// refuses unless force, in which case they are undone too.
func (a *App) Undo(ctx context.Context, id string, change int64, force bool) (*View, error) {
	cur, err := a.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	entries, err := a.History(ctx, id, 0) // newest first
	if err != nil {
		return nil, err
	}
	if change == 0 {
		change = cur.Revision
	}
	at := -1
	for i, e := range entries {
		if e.Revision == change {
			at = i
			break
		}
	}
	switch {
	case at < 0:
		return nil, fmt.Errorf("%w: r%d is not a change in the history of %s", ErrBadInput, change, id)
	case at == len(entries)-1:
		return nil, ErrNothingToUndo
	case at > 0 && !force:
		return nil, &UndoConflictError{Change: change, Later: entries[:at]}
	}
	target := entries[at+1].Revision
	b, err := a.Store.Snapshot(ctx, id, target)
	if errors.Is(err, store.ErrNoSnapshot) {
		return nil, fmt.Errorf("%w: the history no longer holds r%d", ErrNothingToUndo, target)
	}
	if err != nil {
		return nil, err
	}
	what := fmt.Sprintf("r%d", change)
	if at > 0 {
		what = fmt.Sprintf("r%d–r%d", change, entries[0].Revision)
	}
	c := store.ChangeFrom(ctx)
	undone := i18n.Text(entries[at].Summary)
	if m := entries[at].Msg; m != nil {
		undone = i18n.List([]i18n.Msg{*m})
	}
	c.Msg = &i18n.Msg{Key: "change.undid", Args: map[string]i18n.Arg{"what": i18n.Text(what), "change": undone}}
	// Guarded by the revision we read, so a write in between is a conflict, not lost.
	r, err := a.Store.Update(store.WithChange(ctx, c), id, cur.Revision, func(budget.Budget) (budget.Budget, error) { return b, nil })
	if err != nil {
		return nil, err
	}
	return a.view(ctx, r)
}

func sourceLabel(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
