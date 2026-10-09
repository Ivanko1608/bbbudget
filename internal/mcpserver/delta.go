package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"bbbudget/internal/app"
	"bbbudget/internal/budget"
)

// WriteRef is the budget a write applies to, and how much to send back.
type WriteRef struct {
	Ref
	Detail string `json:"detail,omitempty" jsonschema:"what to return: omit for what changed (the default, short), or \"full\" for the whole updated budget"`
}

// deltaResult is what a write (or what_if) returns by default: what the
// change was and what it did to the outcome, not the whole budget again.
type deltaResult struct {
	Budget   string `json:"budget"`
	Revision int64  `json:"revision,omitempty"`
	Saved    bool   `json:"saved"`
	// Change is what was edited, in the user's language.
	Change string `json:"change"`
	// Currency is the base currency of the amounts below.
	Currency string `json:"currency"`
	// Effect is empty when the change didn't move any outcome.
	Effect effect `json:"effect"`
	Hint   string `json:"hint"`
}

type effect struct {
	Totals     map[string]moved       `json:"totals,omitempty"`
	FirstMonth map[string]moved       `json:"first_month,omitempty"`
	Months     *[2]int                `json:"months,omitempty"`
	New        []insightRow           `json:"new_insights,omitempty"`
	Gone       []goneInsight          `json:"gone_insights,omitempty"`
	Updated    []insightRow           `json:"updated_insights,omitempty"`
	Debts      map[string]beforeAfter `json:"debt_payoff,omitempty"`
	Goals      map[string]beforeAfter `json:"goals_reached,omitempty"`
	None       string                 `json:"none,omitempty"`
}

type moved struct {
	Before string `json:"before"`
	After  string `json:"after"`
	Change string `json:"change,omitempty"`
}

type goneInsight struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type beforeAfter struct {
	Before string `json:"before"`
	After  string `json:"after"`
}

const deltaHint = `This is what changed, not the whole budget. get_budget shows any part of it; pass "detail":"full" to a write to get the whole budget back.`

func notWithin(s string) string {
	if s == "" {
		return "not within the window"
	}
	return s
}

func toEffect(d budget.Delta) effect {
	var e effect
	conv := func(m map[string]budget.Moved) map[string]moved {
		if len(m) == 0 {
			return nil
		}
		out := map[string]moved{}
		for k, v := range m {
			mv := moved{Before: v.Before.String(), After: v.After.String()}
			if v.Change.Currency().Code != "" {
				mv.Change = v.Change.String()
			}
			out[k] = mv
		}
		return out
	}
	e.Totals, e.FirstMonth, e.Months = conv(d.Totals), conv(d.FirstMonth), d.Months
	for _, in := range d.Added {
		e.New = append(e.New, newInsightRow(in))
	}
	for _, in := range d.Removed {
		e.Gone = append(e.Gone, goneInsight{in.ID, plain(in.Text)})
	}
	for _, in := range d.Changed {
		e.Updated = append(e.Updated, newInsightRow(in))
	}
	dates := func(m map[string][2]string) map[string]beforeAfter {
		if len(m) == 0 {
			return nil
		}
		out := map[string]beforeAfter{}
		for k, v := range m {
			out[k] = beforeAfter{notWithin(v[0]), notWithin(v[1])}
		}
		return out
	}
	e.Debts, e.Goals = dates(d.Debts), dates(d.Goals)
	if d.Empty() {
		e.None = "no effect on the projection, totals or insights"
	}
	return e
}

// delta builds the default answer for a change from before to after.
func (h *handlers) delta(ctx context.Context, id string, rev int64, saved bool, before, after budget.Budget, afterRep *budget.Report) (any, error) {
	beforeRep, err := h.app.Analyze(ctx, before)
	if err != nil {
		return nil, err
	}
	return deltaResult{
		Budget: id, Revision: rev, Saved: saved,
		Change:   h.app.Localizer(ctx).T(budget.ChangeMsg(before, after)),
		Currency: afterRep.BaseCurrency,
		Effect:   toEffect(budget.Diff(beforeRep, afterRep)),
		Hint:     deltaHint,
	}, nil
}

// written answers a write: what changed, or the whole budget on request.
func (h *handlers) written(ctx context.Context, w WriteRef, v *app.View, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	if w.Detail == "full" || v.Before == nil {
		return summarize(v), nil
	}
	if w.Detail != "" {
		return nil, fmt.Errorf(`detail must be "full" or omitted, got %q`, w.Detail)
	}
	return h.delta(ctx, v.ID, v.Revision, true, *v.Before, v.Budget, v.Report)
}

// Views of get_budget: which top-level fields each one keeps.
var views = map[string][]string{
	"overview": nil, // everything, with months trimmed
	"all":      nil,
	"lines":    {"categories", "lines"},
	"months":   {"months", "totals"},
	"debts":    {"debts", "totals"},
	"goals":    {"goals", "totals"},
	"insights": {"insights"},
}

func sorted(s []string) []string { sort.Strings(s); return s }

// overviewMonths is how many months from the start the overview shows,
// besides the last one.
const overviewMonths = 3

// view trims a summary to one view and an optional month range.
func view(s Summary, name, from, to string) (any, error) {
	if name == "" {
		name = "overview"
	}
	keep, ok := views[name]
	if !ok {
		var names []string
		for k := range views {
			names = append(names, k)
		}
		return nil, fmt.Errorf("view must be one of %s", strings.Join(sorted(names), ", "))
	}
	months := s.Months
	if from != "" || to != "" {
		var out []monthRow
		for _, m := range months {
			if (from == "" || m.Month >= from) && (to == "" || m.Month <= to) {
				out = append(out, m)
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("no months between %q and %q; the budget covers %s to %s", from, to, months[0].Month, months[len(months)-1].Month)
		}
		months = out
	}
	note := ""
	if name == "overview" && from == "" && to == "" && len(months) > overviewMonths+1 {
		note = fmt.Sprintf("showing months 1-%d and the last of %d; use view \"months\" (optionally with from/to) for the rest", overviewMonths, len(months))
		months = append(append([]monthRow{}, months[:overviewMonths]...), months[len(months)-1])
	}
	s.Months = months
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var all map[string]any
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, err
	}
	if keep != nil {
		out := map[string]any{"id": all["id"], "name": all["name"], "revision": all["revision"], "view": name}
		if c, ok := all["settings"].(map[string]any); ok {
			out["base_currency"] = c["base_currency"]
		}
		for _, k := range keep {
			out[k] = all[k]
		}
		all = out
	}
	if note != "" {
		all["months_note"] = note
	}
	return all, nil
}
