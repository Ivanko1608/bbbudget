package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"bbbudget/internal/budget"
	"bbbudget/internal/i18n"
	"bbbudget/internal/store"
)

// History returns a budget's recent changes, newest first: at most limit of
// them, or store.AllEntries. Summaries are in English; see LocalizeHistory.
func (a *App) History(ctx context.Context, id string, limit int) ([]store.Entry, error) {
	return a.Store.History(ctx, id, limit)
}

// LocalizeHistory renders every summary in history with l.
func LocalizeHistory(history []store.Entry, l *i18n.Localizer) {
	for i, entry := range history {
		history[i].Summary = l.Render(summaryMsg(entry))
	}
}

// summaryMsg says what an entry changed, from what the store recorded about it.
func summaryMsg(entry store.Entry) i18n.Msg {
	changes := budget.DifferencesMsg(entry.Changes)
	switch {
	case entry.Source == store.SourceEarlier:
		return i18n.Msg{Key: "change.baseline"}
	case entry.Revision == 1:
		return i18n.Msg{Key: "change.created"}
	case entry.Undoes != 0:
		undone := i18n.Text(fmt.Sprintf("r%d", entry.Undoes))
		args := map[string]i18n.Arg{"what": undone, "change": i18n.Nested(changes)}
		return i18n.Msg{Key: "change.undid", Args: args}
	default:
		return changes
	}
}

// UndoConflictError means undoing a change would also undo the changes made
// after it, and the caller didn't agree to that.
type UndoConflictError struct {
	Revision int64
	Later    []store.Entry // newest first
}

func (e *UndoConflictError) Error() string {
	later := make([]string, len(e.Later))
	for i, entry := range e.Later {
		who := sourceLabel(entry.Source)
		later[i] = fmt.Sprintf("r%d (%s): %s", entry.Revision, who, entry.Summary)
	}
	return fmt.Sprintf("undoing r%d would also undo %d later change(s): %s. "+
		"Undo those first, or force it to undo them all",
		e.Revision, len(e.Later), strings.Join(later, "; "))
}

func sourceLabel(s store.Source) string {
	if s == "" {
		return "unknown"
	}
	return string(s)
}

// ErrNothingToUndo means the change created the budget: there is no earlier
// version to go back to.
var ErrNothingToUndo = errors.New("nothing to undo: that change created the budget")

// ErrTooOld means the version before the change has aged out of the history.
var ErrTooOld = errors.New(
	"can't undo that far back: the history no longer keeps the version before that change")

// LatestRevision in an UndoRequest means the most recent change.
const LatestRevision = 0

// SeenUnchecked in an UndoRequest means the caller didn't say which changes
// it was shown, so a forced undo isn't checked against them.
const SeenUnchecked = 0

// UndoRequest says which change to undo and how far the caller agreed to go.
type UndoRequest struct {
	// Revision is the change to undo, or LatestRevision.
	Revision int64
	// Force also undoes every change made after it. Without it, Undo refuses
	// when there are any, and lists them.
	Force bool
	// Seen is the newest revision the caller was shown when it chose to
	// force. If more changes have landed since, Undo refuses again with the
	// up-to-date list, so a forced undo never drops a change nobody saw.
	Seen int64
}

// Undo reverts a change by saving the version from just before it as a new
// revision, so an undo can itself be undone. Going back to that version also
// throws away every later change, so those are only undone with Force.
func (a *App) Undo(
	ctx context.Context, origin store.Origin, id string, req UndoRequest,
) (*View, error) {
	latest, err := a.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	plan, err := a.planUndo(ctx, id, latest.Revision, req)
	if err != nil {
		return nil, err
	}
	old, err := a.Store.Snapshot(ctx, id, plan.restore)
	if errors.Is(err, store.ErrNoSnapshot) {
		return nil, ErrTooOld // it aged out after planUndo read the history
	}
	if err != nil {
		return nil, err
	}
	origin.Undoes = plan.revision
	// Expecting the revision the plan was made against turns a write that
	// lands in between into a conflict, instead of undoing it unseen.
	return a.update(ctx, origin, id, latest.Revision, replaceWith(old))
}

// undoPlan is the change an undo reverts and the revision it restores.
type undoPlan struct {
	revision int64
	restore  int64
}

func (a *App) planUndo(
	ctx context.Context, id string, latest int64, req UndoRequest,
) (undoPlan, error) {
	revision := req.Revision
	if revision == LatestRevision {
		revision = latest
	}
	history, err := a.Store.History(ctx, id, store.AllEntries)
	if err != nil {
		return undoPlan{}, err
	}
	change, found := findChange(history, revision)
	if !found {
		err = NewClientError(ErrBadInput, "r%d is not a change in the history of %s", revision, id)
		return undoPlan{}, err
	}
	if err := checkUndoable(change.entry); err != nil {
		return undoPlan{}, err
	}
	if refusesLaterChanges(req, change.later, latest) {
		return undoPlan{}, &UndoConflictError{Revision: revision, Later: change.later}
	}
	return undoPlan{revision: revision, restore: change.previous.Revision}, nil
}

// placedChange is a change with its neighbours in the history.
type placedChange struct {
	entry    store.Entry
	later    []store.Entry // newest first
	previous store.Entry   // the version an undo goes back to
}

// findChange finds revision in a newest-first history.
func findChange(history []store.Entry, revision int64) (placedChange, bool) {
	i := slices.IndexFunc(history, func(e store.Entry) bool { return e.Revision == revision })
	if i < 0 {
		return placedChange{}, false
	}
	change := placedChange{entry: history[i], later: history[:i]}
	if i+1 < len(history) {
		change.previous = history[i+1]
	}
	return change, true
}

func checkUndoable(target store.Entry) error {
	createdTheBudget := target.Revision == 1 || target.Source == store.SourceEarlier
	switch {
	case target.Undoable:
		return nil
	case createdTheBudget:
		return ErrNothingToUndo
	default:
		return ErrTooOld
	}
}

// refusesLaterChanges reports whether undoing would throw away later
// changes the caller didn't agree to throw away.
func refusesLaterChanges(req UndoRequest, later []store.Entry, latest int64) bool {
	if len(later) == 0 {
		return false
	}
	if !req.Force {
		return true
	}
	return req.Seen != SeenUnchecked && req.Seen != latest
}
