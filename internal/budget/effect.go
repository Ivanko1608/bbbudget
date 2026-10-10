package budget

import (
	"maps"
	"slices"

	"bbbudget/internal/money"
)

// Change is one value before and after an edit.
type Change[T any] struct {
	Before T `json:"before"`
	After  T `json:"after"`
}

// AmountChange is a figure before and after an edit, and By how much it
// moved. By has no currency when the base currency changed, because amounts
// in two currencies have no difference.
type AmountChange struct {
	Change[money.Money]
	By money.Money `json:"by"`
}

// TotalsChange holds the window totals that moved; the rest are nil.
type TotalsChange struct {
	Income       *AmountChange
	Expenses     *AmountChange
	DebtPayments *AmountChange
	Saved        *AmountChange
	EndSavings   *AmountChange
	Interest     *AmountChange
}

// MonthChange holds the figures of one month that moved; the rest are nil.
type MonthChange struct {
	Income   *AmountChange
	Expenses *AmountChange
	Left     *AmountChange
	Saved    *AmountChange
}

// InsightChanges are the insights that appeared, went away or now say
// something else, matched by id.
type InsightChanges struct {
	Added   []Insight
	Removed []Insight
	Changed []Insight
}

// GoalReach is when a goal is reached: in Month ("YYYY-MM"), AlreadyMet at
// the start, or neither when not within the window.
type GoalReach struct {
	Month      string
	AlreadyMet bool
}

// Effect is what an edit did to the outcome. It holds only what moved.
type Effect struct {
	Totals     TotalsChange
	FirstMonth MonthChange
	// Months is set when the length of the projection changed.
	Months   *Change[int]
	Insights InsightChanges
	// DebtPayoffs maps a debt's name to the month ("YYYY-MM") it is paid
	// off, or "" when not within the window.
	DebtPayoffs  map[string]Change[string]
	GoalsReached map[string]Change[GoalReach]
}

// EffectOf compares the reports of a budget before and after an edit.
func EffectOf(before, after *Report) Effect {
	return Effect{
		Totals:       totalsChange(before.Totals, after.Totals),
		FirstMonth:   monthChange(before.Months[0], after.Months[0]),
		Months:       lengthChange(len(before.Months), len(after.Months)),
		Insights:     insightChanges(before.Insights, after.Insights),
		DebtPayoffs:  changedValues(debtPayoffs(before), debtPayoffs(after)),
		GoalsReached: changedValues(goalsReached(before), goalsReached(after)),
	}
}

// None reports whether the edit changed nothing in the outcome.
func (e Effect) None() bool {
	numbersStayed := e.Totals == TotalsChange{} && e.FirstMonth == MonthChange{} && e.Months == nil
	insightsStayed := len(e.Insights.Added)+len(e.Insights.Removed)+len(e.Insights.Changed) == 0
	datesStayed := len(e.DebtPayoffs)+len(e.GoalsReached) == 0
	return numbersStayed && insightsStayed && datesStayed
}

func totalsChange(before, after Totals) TotalsChange {
	return TotalsChange{
		Income:       amountChange(before.Income, after.Income),
		Expenses:     amountChange(before.Expenses, after.Expenses),
		DebtPayments: amountChange(before.DebtPayments, after.DebtPayments),
		Saved:        amountChange(before.Saved, after.Saved),
		EndSavings:   amountChange(before.EndSavings, after.EndSavings),
		Interest:     amountChange(before.TotalInterest, after.TotalInterest),
	}
}

func monthChange(before, after Month) MonthChange {
	return MonthChange{
		Income:   amountChange(before.Income, after.Income),
		Expenses: amountChange(before.Expenses, after.Expenses),
		Left:     amountChange(before.Left, after.Left),
		Saved:    amountChange(before.Saved, after.Saved),
	}
}

// amountChange is nil when the amount stayed the same.
func amountChange(before, after money.Money) *AmountChange {
	sameCurrency := before.Currency() == after.Currency()
	if sameCurrency && before.Cmp(after) == 0 {
		return nil
	}
	moved := &AmountChange{Change: Change[money.Money]{Before: before, After: after}}
	if sameCurrency {
		moved.By = after.Sub(before)
	}
	return moved
}

func lengthChange(before, after int) *Change[int] {
	if before == after {
		return nil
	}
	return &Change[int]{Before: before, After: after}
}

func insightChanges(before, after []Insight) InsightChanges {
	var changes InsightChanges
	for _, now := range after {
		earlier, existed := findInsight(before, now.ID)
		if !existed {
			changes.Added = append(changes.Added, now)
		} else if !sameInsight(earlier, now) {
			changes.Changed = append(changes.Changed, now)
		}
	}
	for _, earlier := range before {
		if _, remains := findInsight(after, earlier.ID); !remains {
			changes.Removed = append(changes.Removed, earlier)
		}
	}
	return changes
}

func findInsight(insights []Insight, id string) (Insight, bool) {
	i := slices.IndexFunc(insights, func(in Insight) bool { return in.ID == id })
	if i < 0 {
		return Insight{}, false
	}
	return insights[i], true
}

// sameInsight compares the messages, not their text, so it holds in any
// language. Rendering both in English compares every argument exactly.
func sameInsight(a, b Insight) bool {
	return a.Tone == b.Tone && english().Render(a.Msg) == english().Render(b.Msg)
}

func debtPayoffs(r *Report) map[string]string {
	payoffs := map[string]string{}
	for _, d := range r.Debts {
		payoffs[d.Name] = r.monthKey(d.PaidOffMonth)
	}
	return payoffs
}

func goalsReached(r *Report) map[string]GoalReach {
	reached := map[string]GoalReach{}
	for _, g := range r.Goals {
		reached[g.Name] = GoalReach{Month: r.monthKey(g.ReachedMonth), AlreadyMet: g.AlreadyMet}
	}
	return reached
}

// monthKey is the "YYYY-MM" of a month index, or "" for none.
func (r *Report) monthKey(index *int) string {
	if index == nil {
		return ""
	}
	return r.Months[*index].Key
}

// changedValues pairs up the values that differ, by name. A name only one
// side has pairs with the zero value.
func changedValues[T comparable](before, after map[string]T) map[string]Change[T] {
	names := slices.Concat(slices.Collect(maps.Keys(before)), slices.Collect(maps.Keys(after)))
	changes := map[string]Change[T]{}
	for _, name := range names {
		if before[name] != after[name] {
			changes[name] = Change[T]{Before: before[name], After: after[name]}
		}
	}
	return changes
}
