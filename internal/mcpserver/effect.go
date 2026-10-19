package mcpserver

import (
	"fmt"

	"bbbudget/internal/app"
	"bbbudget/internal/budget"
	"bbbudget/internal/i18n"
	"bbbudget/internal/money"
)

// Detail is how much a write sends back.
type Detail string

const (
	// DetailChange is what changed and what that did: the default, short.
	DetailChange Detail = ""
	// DetailFull is the whole updated budget.
	DetailFull Detail = "full"
)

func (d Detail) check() error {
	if d != DetailChange && d != DetailFull {
		return fmt.Errorf(`detail must be "full" or omitted, got %q`, d)
	}
	return nil
}

// WriteRef is the budget a write applies to, and how much to send back.
type WriteRef struct {
	Ref
	Detail Detail `json:"detail,omitempty" jsonschema:"what to return: omit for what changed (short), or \"full\" for the whole updated budget"`
}

// changeOutcome is what a change was and what it did to the outcome.
type changeOutcome struct {
	// Change is what was edited, in the user's language.
	Change string `json:"change"`
	// Currency is the base currency of the amounts in Effect.
	Currency string `json:"currency"`
	Effect   effect `json:"effect"`
}

// changeResult is what a write or what_if returns by default: what the
// change was and what it did to the outcome, not the whole budget again.
type changeResult struct {
	Budget   string `json:"budget"`
	Revision int64  `json:"revision,omitempty"`
	Saved    bool   `json:"saved"`
	changeOutcome
	Hint string `json:"hint"`
}

const changeHint = `This is what changed, not the whole budget. get_budget shows any ` +
	`part of it; pass "detail":"full" to a write to get the whole budget back.`

// effect is budget.Effect with amounts as exact strings and insights in
// the user's language.
type effect struct {
	Totals       totalsChange                        `json:"totals,omitzero"`
	FirstMonth   monthChange                         `json:"first_month,omitzero"`
	Months       *budget.Change[int]                 `json:"months,omitempty"`
	Insights     insightChanges                      `json:"insights,omitzero"`
	DebtPayoffs  map[string]budget.Change[string]    `json:"debt_payoffs,omitempty"`
	GoalsReached map[string]budget.Change[goalReach] `json:"goals_reached,omitempty"`
	None         bool                                `json:"none,omitempty"`
}

type totalsChange struct {
	Income       *amountChange `json:"income,omitempty"`
	Expenses     *amountChange `json:"expenses,omitempty"`
	DebtPayments *amountChange `json:"debt_payments,omitempty"`
	Saved        *amountChange `json:"saved,omitempty"`
	EndSavings   *amountChange `json:"end_savings,omitempty"`
	Interest     *amountChange `json:"interest,omitempty"`
}

type monthChange struct {
	Income   *amountChange `json:"income,omitempty"`
	Expenses *amountChange `json:"expenses,omitempty"`
	Left     *amountChange `json:"left,omitempty"`
	Saved    *amountChange `json:"saved,omitempty"`
}

type amountChange struct {
	Before string `json:"before"`
	After  string `json:"after"`
	By     string `json:"by,omitempty"`
}

type insightChanges struct {
	Added   []insightRow `json:"added,omitempty"`
	Removed []insightRow `json:"removed,omitempty"`
	Changed []insightRow `json:"changed,omitempty"`
}

// notWithinWindow stands for a payoff or goal date past the projection.
const notWithinWindow = "not within the window"

// goalReach is a goal's month ("YYYY-MM"), goalAlreadyMet or notWithinWindow.
type goalReach string

const goalAlreadyMet goalReach = "already met"

// changeOf describes an edit from before to after, whose report is
// afterReport, in the user's language.
func (h *handlers) changeOf(
	before, after budget.Budget, afterReport *budget.Report,
) (changeOutcome, error) {
	beforeReport, err := budget.Analyze(before)
	if err != nil {
		return changeOutcome{}, err
	}
	reader := h.localizer()
	differences := budget.Differences(before, after)
	return changeOutcome{
		Change:   reader.Render(budget.DifferencesMsg(differences)),
		Currency: afterReport.BaseCurrency,
		Effect:   newEffect(budget.EffectOf(beforeReport, afterReport), reader),
	}, nil
}

// written answers a write: what changed, or the whole budget on request.
func (h *handlers) written(w WriteRef, v *app.View, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	if err := w.Detail.check(); err != nil {
		return nil, err
	}
	nothingSaved := v.Before == nil
	if w.Detail == DetailFull || nothingSaved {
		return h.sum(v, nil)
	}
	outcome, err := h.changeOf(*v.Before, v.Budget, v.Report)
	if err != nil {
		return nil, err
	}
	return changeResult{
		Budget:        v.ID,
		Revision:      v.Revision,
		Saved:         true,
		changeOutcome: outcome,
		Hint:          changeHint,
	}, nil
}

// previewed answers what_if: what the ops would change, or the whole
// hypothetical budget on request. Nothing is saved.
func (h *handlers) previewed(w WriteRef, v *app.View) (any, error) {
	if err := w.Detail.check(); err != nil {
		return nil, err
	}
	if w.Detail == DetailFull {
		v.Localize(h.localizer())
		whole := summarize(v)
		whole.ID = v.ID + " (what-if, not saved)"
		whole.Revision = 0
		return whole, nil
	}
	outcome, err := h.changeOf(*v.Before, v.Budget, v.Report)
	if err != nil {
		return nil, err
	}
	return changeResult{Budget: v.ID, changeOutcome: outcome, Hint: changeHint}, nil
}

func newEffect(e budget.Effect, reader *i18n.Localizer) effect {
	return effect{
		Totals:       newTotalsChange(e.Totals),
		FirstMonth:   newMonthChange(e.FirstMonth),
		Months:       e.Months,
		Insights:     newInsightChanges(e.Insights, reader),
		DebtPayoffs:  monthsOrNotWithin(e.DebtPayoffs),
		GoalsReached: newGoalsReached(e.GoalsReached),
		None:         e.None(),
	}
}

func newTotalsChange(t budget.TotalsChange) totalsChange {
	return totalsChange{
		Income:       newAmountChange(t.Income),
		Expenses:     newAmountChange(t.Expenses),
		DebtPayments: newAmountChange(t.DebtPayments),
		Saved:        newAmountChange(t.Saved),
		EndSavings:   newAmountChange(t.EndSavings),
		Interest:     newAmountChange(t.Interest),
	}
}

func newMonthChange(m budget.MonthChange) monthChange {
	return monthChange{
		Income:   newAmountChange(m.Income),
		Expenses: newAmountChange(m.Expenses),
		Left:     newAmountChange(m.Left),
		Saved:    newAmountChange(m.Saved),
	}
}

func newAmountChange(c *budget.AmountChange) *amountChange {
	if c == nil {
		return nil
	}
	return &amountChange{Before: c.Before.String(), After: c.After.String(), By: amountOrNone(c.By)}
}

// amountOrNone is "" for an amount without a currency.
func amountOrNone(m money.Money) string {
	if m.Currency().Code == "" {
		return ""
	}
	return m.String()
}

func newInsightChanges(c budget.InsightChanges, reader *i18n.Localizer) insightChanges {
	return insightChanges{
		Added:   insightRows(c.Added, reader),
		Removed: insightRows(c.Removed, reader),
		Changed: insightRows(c.Changed, reader),
	}
}

func insightRows(insights []budget.Insight, reader *i18n.Localizer) []insightRow {
	var rows []insightRow
	for _, in := range insights {
		in.Text = reader.Render(in.Msg)
		rows = append(rows, newInsightRow(in))
	}
	return rows
}

func monthsOrNotWithin(payoffs map[string]budget.Change[string]) map[string]budget.Change[string] {
	shown := map[string]budget.Change[string]{}
	for name, c := range payoffs {
		shown[name] = budget.Change[string]{
			Before: monthOrNotWithin(c.Before),
			After:  monthOrNotWithin(c.After),
		}
	}
	return shown
}

func monthOrNotWithin(month string) string {
	if month == "" {
		return notWithinWindow
	}
	return month
}

func newGoalsReached(
	goals map[string]budget.Change[budget.GoalReach],
) map[string]budget.Change[goalReach] {
	shown := map[string]budget.Change[goalReach]{}
	for name, c := range goals {
		shown[name] = budget.Change[goalReach]{
			Before: newGoalReach(c.Before),
			After:  newGoalReach(c.After),
		}
	}
	return shown
}

func newGoalReach(r budget.GoalReach) goalReach {
	switch {
	case r.AlreadyMet:
		return goalAlreadyMet
	case r.Month == "":
		return notWithinWindow
	}
	return goalReach(r.Month)
}
