package app

import (
	"context"
	"fmt"

	"bbbudget/internal/budget"
	"bbbudget/internal/store"
)

// WhatsNew is what happened to a budget since a revision.
type WhatsNew struct {
	ID string
	// Since is the revision compared against; Revision is the current one.
	Since, Revision int64
	// Others are the changes since then made by someone other than the
	// asker, newest first.
	Others []store.Entry
	// Before is the budget as of Since, and Now the current view.
	Before budget.Budget
	Now    *View
	// Month is this month's plan versus actual, if today is in the window.
	Month *budget.MonthStatus
}

// WhatsNew reports what changed since a revision: by default since the last
// change made by the asker (the source on ctx, e.g. "mcp"), so an assistant
// sees what happened while it wasn't looking.
func (a *App) WhatsNew(ctx context.Context, id string, since int64) (*WhatsNew, error) {
	v, err := a.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	h, err := a.History(ctx, id, 0) // newest first
	if err != nil {
		return nil, err
	}
	me := store.ChangeFrom(ctx).Source
	if since == 0 {
		for _, e := range h {
			if e.Source == me {
				since = e.Revision
				break
			}
		}
		if since == 0 && len(h) > 0 {
			since = h[len(h)-1].Revision // never written here: since the start of the history
		}
	}
	if since > v.Revision {
		return nil, fmt.Errorf("%w: r%d is newer than the budget (r%d)", ErrBadInput, since, v.Revision)
	}
	// The state as of since is the newest kept entry at or before it.
	base := int64(0)
	for _, e := range h {
		if e.Revision <= since {
			base = e.Revision
			break
		}
	}
	if base == 0 {
		return nil, fmt.Errorf("%w: the history no longer reaches back to r%d", ErrBadInput, since)
	}
	before, err := a.Store.Snapshot(ctx, id, base)
	if err != nil {
		return nil, err
	}
	out := &WhatsNew{ID: id, Since: since, Revision: v.Revision, Before: before, Now: v, Others: []store.Entry{}}
	for _, e := range h {
		if e.Revision > since && e.Source != me {
			out.Others = append(out.Others, e)
		}
	}
	if st, err := a.MonthStatus(ctx, id, ""); err == nil {
		out.Month = st
	}
	return out, nil
}
