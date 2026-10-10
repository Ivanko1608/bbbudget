package mcpserver

import (
	"fmt"
	"slices"
	"strings"
)

// View is the part of a budget get_budget returns.
type View string

const (
	// ViewOverview is everything, with the months trimmed to the first few
	// and the last.
	ViewOverview View = "overview"
	ViewAll      View = "all"
	ViewItems    View = "items"
	ViewMonths   View = "months"
	ViewDebts    View = "debts"
	ViewGoals    View = "goals"
	ViewInsights View = "insights"
)

var views = []View{ViewOverview, ViewAll, ViewItems, ViewMonths, ViewDebts, ViewGoals, ViewInsights}

// overviewMonths is how many months from the start the overview shows,
// besides the last one.
const overviewMonths = 3

// GetIn picks the budget, the part of it and the months get_budget returns.
type GetIn struct {
	Ref
	View View   `json:"view,omitempty" jsonschema:"which part: overview (default: everything, months trimmed to the first few and the last), all, items, months, debts, goals or insights"`
	From string `json:"from,omitempty" jsonschema:"first month to include, YYYY-MM"`
	To   string `json:"to,omitempty" jsonschema:"last month to include, YYYY-MM"`
}

// part trims a summary to the view and month range in.
func part(s Summary, in GetIn) (Summary, error) {
	view := in.View
	if view == "" {
		view = ViewOverview
	}
	if !slices.Contains(views, view) {
		return Summary{}, fmt.Errorf("view must be one of %s", viewNames())
	}
	months, err := monthsBetween(s.Months, in.From, in.To)
	if err != nil {
		return Summary{}, err
	}
	s.Months = months
	wholeWindow := in.From == "" && in.To == ""
	if view == ViewOverview && wholeWindow {
		s = trimMonths(s)
	}
	return keepView(s, view), nil
}

func viewNames() string {
	names := make([]string, len(views))
	for i, v := range views {
		names[i] = string(v)
	}
	return strings.Join(names, ", ")
}

// monthsBetween keeps the months from from to to, both "YYYY-MM" and
// inclusive; "" leaves that end open.
func monthsBetween(months []monthRow, from, to string) ([]monthRow, error) {
	if from == "" && to == "" {
		return months, nil
	}
	var kept []monthRow
	for _, m := range months {
		afterFrom := from == "" || m.Month >= from
		beforeTo := to == "" || m.Month <= to
		if afterFrom && beforeTo {
			kept = append(kept, m)
		}
	}
	if len(kept) == 0 {
		first, last := months[0].Month, months[len(months)-1].Month
		return nil, fmt.Errorf("no months between %q and %q; the budget covers %s to %s",
			from, to, first, last)
	}
	return kept, nil
}

// trimMonths keeps the first overviewMonths months and the last one.
func trimMonths(s Summary) Summary {
	if len(s.Months) <= overviewMonths+1 {
		return s
	}
	last := s.Months[len(s.Months)-1]
	s.MonthsNote = fmt.Sprintf(`showing months 1-%d and the last of %d; `+
		`use view "months" (optionally with from/to) for the rest`, overviewMonths, len(s.Months))
	s.Months = append(slices.Clone(s.Months[:overviewMonths]), last)
	return s
}

// keepView keeps the fields a view shows, and the budget's id, name,
// revision and base currency.
func keepView(s Summary, view View) Summary {
	kept := Summary{
		ID:           s.ID,
		Revision:     s.Revision,
		Name:         s.Name,
		View:         view,
		BaseCurrency: s.Settings.BaseCurrency,
	}
	switch view {
	case ViewOverview, ViewAll:
		s.View = view
		return s
	case ViewItems:
		kept.Categories = s.Categories
		kept.Items = s.Items
	case ViewMonths:
		kept.Months = s.Months
		kept.Totals = s.Totals
	case ViewDebts:
		kept.Debts = s.Debts
		kept.Totals = s.Totals
	case ViewGoals:
		kept.Pots = s.Pots
		kept.Goals = s.Goals
		kept.Totals = s.Totals
	case ViewInsights:
		kept.Insights = s.Insights
	}
	return kept
}
