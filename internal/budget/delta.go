package budget

import "bbbudget/internal/money"

// Delta is what a change did to the outcome: the numbers that moved, the
// insights that appeared, disappeared or changed, and the payoff and goal
// dates that moved. Both reports must be localized the same way.
type Delta struct {
	// Totals and FirstMonth hold only the figures that changed, base currency.
	Totals     map[string]Moved `json:"totals,omitempty"`
	FirstMonth map[string]Moved `json:"first_month,omitempty"`
	// Months is set when the projection length changed.
	Months *[2]int `json:"months,omitempty"`
	// Insights are matched by id.
	Added   []Insight `json:"insights_added,omitempty"`
	Removed []Insight `json:"insights_removed,omitempty"`
	Changed []Insight `json:"insights_changed,omitempty"`
	// Debts and Goals map a name to its payoff or reached date, before and
	// after ("" = not within the window).
	Debts map[string][2]string `json:"debts,omitempty"`
	Goals map[string][2]string `json:"goals,omitempty"`
}

// Moved is one figure before and after.
type Moved struct {
	Before money.Money `json:"before"`
	After  money.Money `json:"after"`
	Change money.Money `json:"change"`
}

// Empty reports whether nothing in the outcome changed.
func (d Delta) Empty() bool {
	return len(d.Totals) == 0 && len(d.FirstMonth) == 0 && d.Months == nil && len(d.Added) == 0 &&
		len(d.Removed) == 0 && len(d.Changed) == 0 && len(d.Debts) == 0 && len(d.Goals) == 0
}

// Diff compares two reports of the same budget.
func Diff(before, after *Report) Delta {
	var d Delta
	moved := func(into *map[string]Moved, name string, a, b money.Money) {
		same := a.Currency() == b.Currency()
		if same && a.Cmp(b) == 0 {
			return
		}
		if *into == nil {
			*into = map[string]Moved{}
		}
		m := Moved{Before: a, After: b}
		if same { // a new base currency has no meaningful difference
			m.Change = b.Sub(a)
		}
		(*into)[name] = m
	}
	bt, at := before.Totals, after.Totals
	moved(&d.Totals, "income", bt.Income, at.Income)
	moved(&d.Totals, "expenses", bt.Expenses, at.Expenses)
	moved(&d.Totals, "debt_payments", bt.DebtPayments, at.DebtPayments)
	moved(&d.Totals, "saved", bt.Saved, at.Saved)
	moved(&d.Totals, "end_savings", bt.EndSavings, at.EndSavings)
	moved(&d.Totals, "interest", bt.TotalInterest, at.TotalInterest)
	if len(before.Months) > 0 && len(after.Months) > 0 {
		b0, a0 := before.Months[0], after.Months[0]
		moved(&d.FirstMonth, "income", b0.Income, a0.Income)
		moved(&d.FirstMonth, "expenses", b0.Expenses, a0.Expenses)
		moved(&d.FirstMonth, "left", b0.Left, a0.Left)
		moved(&d.FirstMonth, "saved", b0.Saved, a0.Saved)
	}
	if len(before.Months) != len(after.Months) {
		d.Months = &[2]int{len(before.Months), len(after.Months)}
	}

	old := map[string]Insight{}
	for _, in := range before.Insights {
		old[in.ID] = in
	}
	seen := map[string]bool{}
	for _, in := range after.Insights {
		seen[in.ID] = true
		prev, ok := old[in.ID]
		switch {
		case !ok:
			d.Added = append(d.Added, in)
		case prev.Text != in.Text || prev.Tone != in.Tone:
			d.Changed = append(d.Changed, in)
		}
	}
	for _, in := range before.Insights {
		if !seen[in.ID] {
			d.Removed = append(d.Removed, in)
		}
	}

	dates := func(into *map[string][2]string, names []string, b, a map[string]string) {
		for _, n := range names {
			if b[n] != a[n] {
				if *into == nil {
					*into = map[string][2]string{}
				}
				(*into)[n] = [2]string{b[n], a[n]}
			}
		}
	}
	debtDates := func(r *Report) (map[string]string, []string) {
		m, names := map[string]string{}, []string{}
		for _, s := range r.Debts {
			m[s.Name], names = s.PaidOffLabel, append(names, s.Name)
		}
		return m, names
	}
	goalDates := func(r *Report) (map[string]string, []string) {
		m, names := map[string]string{}, []string{}
		for _, g := range r.Goals {
			label := g.ReachedLabel
			if g.AlreadyMet {
				label = "already met"
			}
			m[g.Name], names = label, append(names, g.Name)
		}
		return m, names
	}
	bd, bn := debtDates(before)
	ad, an := debtDates(after)
	dates(&d.Debts, union(bn, an), bd, ad)
	bg, gn := goalDates(before)
	ag, agn := goalDates(after)
	dates(&d.Goals, union(gn, agn), bg, ag)
	return d
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string{}, a...), b...) {
		if !seen[s] {
			seen[s], out = true, append(out, s)
		}
	}
	return out
}
