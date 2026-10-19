package app

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"bbbudget/internal/budget"
	"bbbudget/internal/store"
)

// FromOwnLastChange asks WhatsNew to start from the asker's own last change.
const FromOwnLastChange int64 = 0

// WhatsNewRequest says who is asking, and from which revision to compare.
type WhatsNewRequest struct {
	Asker store.Source
	// FromRevision is a revision, or FromOwnLastChange.
	FromRevision int64
}

// WhatsNew is what happened to a budget since a revision.
type WhatsNew struct {
	// FromRevision is the revision compared against.
	FromRevision int64
	// ByOthers are the changes after FromRevision that someone other than the
	// asker made, newest first. Summaries are in English.
	ByOthers []store.Entry
	// Before is the budget as of FromRevision.
	Before budget.Budget
	Now    *View
	// ThisMonth is this month's plan versus actual, or nil when this month
	// is outside the budget's window.
	ThisMonth *budget.MonthStatus
}

// WhatsNew reports what changed in a budget since a revision, so an
// assistant coming back to it sees what happened while it wasn't looking.
func (a *App) WhatsNew(ctx context.Context, id string, req WhatsNewRequest) (*WhatsNew, error) {
	now, err := a.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	history, err := a.History(ctx, id, store.AllEntries)
	if err != nil {
		return nil, err
	}
	fromRevision := req.FromRevision
	if fromRevision == FromOwnLastChange {
		ownLastChange, found := findOwnLastChange(history, req.Asker)
		if !found {
			return nil, fmt.Errorf("%w: the budget has no history", ErrBadInput)
		}
		fromRevision = ownLastChange
	}
	before, err := a.budgetAsOf(ctx, id, history, fromRevision, now.Revision)
	if err != nil {
		return nil, err
	}
	thisMonth, err := a.thisMonthStatus(ctx, id)
	if err != nil {
		return nil, err
	}
	return &WhatsNew{
		FromRevision: fromRevision,
		ByOthers:     listChangesByOthers(history, fromRevision, req.Asker),
		Before:       before,
		Now:          now,
		ThisMonth:    thisMonth,
	}, nil
}

// findOwnLastChange is the revision of the asker's newest change in history
// (newest first), or of the oldest change kept when the asker made none.
// It finds nothing only when the history is empty.
func findOwnLastChange(history []store.Entry, asker store.Source) (revision int64, found bool) {
	if len(history) == 0 {
		return 0, false
	}
	byAsker := func(entry store.Entry) bool { return entry.Source == asker }
	if i := slices.IndexFunc(history, byAsker); i >= 0 {
		return history[i].Revision, true
	}
	return history[len(history)-1].Revision, true
}

// budgetAsOf is the budget as it was at fromRevision: the version saved
// by the newest change at or before it.
func (a *App) budgetAsOf(
	ctx context.Context, id string, history []store.Entry, fromRevision, current int64,
) (budget.Budget, error) {
	if fromRevision > current {
		return budget.Budget{}, fmt.Errorf("%w: r%d is newer than the budget (r%d)",
			ErrBadInput, fromRevision, current)
	}
	notLater := func(entry store.Entry) bool { return entry.Revision <= fromRevision }
	i := slices.IndexFunc(history, notLater)
	if i < 0 {
		return budget.Budget{}, fmt.Errorf("%w: the history no longer reaches back to r%d",
			ErrBadInput, fromRevision)
	}
	return a.Store.Snapshot(ctx, id, history[i].Revision)
}

func listChangesByOthers(
	history []store.Entry, fromRevision int64, asker store.Source,
) []store.Entry {
	others := []store.Entry{}
	for _, entry := range history {
		if entry.Revision > fromRevision && entry.Source != asker {
			others = append(others, entry)
		}
	}
	return others
}

func (a *App) thisMonthStatus(ctx context.Context, id string) (*budget.MonthStatus, error) {
	status, err := a.MonthStatus(ctx, id, "")
	if errors.Is(err, budget.ErrOutsideWindow) {
		return nil, nil
	}
	return status, err
}
