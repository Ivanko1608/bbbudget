package mcpserver

import (
	"fmt"
	"slices"

	"bbbudget/internal/app"
	"bbbudget/internal/budget"
	"bbbudget/internal/i18n"
	"bbbudget/internal/money"
)

// Summary is the compact, LLM-friendly view of a budget. Money is rendered as
// exact decimal strings; the currency is stated once (base) or per field.
type Summary struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision,omitempty"`
	Name     string `json:"name"`
	Note     string `json:"note,omitempty"`
	// View is the part of the budget get_budget was asked for.
	View View `json:"view,omitempty"`
	// BaseCurrency repeats the settings' one in views without settings.
	BaseCurrency string              `json:"base_currency,omitempty"`
	Settings     settingsRow         `json:"settings,omitzero"`
	Categories   []budget.Category   `json:"categories,omitempty"`
	Items        []itemRow           `json:"items,omitempty"`
	Debts        []debtRow           `json:"debts,omitempty"`
	Pots         []potRow            `json:"pots,omitempty"`
	Withdrawals  []budget.Withdrawal `json:"withdrawals,omitempty"`
	Goals        []goalRow           `json:"goals,omitempty"`
	// Shortfalls: months that fall short and the pots that could cover them.
	// Savings are never used automatically; suggest add_withdrawal.
	Shortfalls []shortfallRow `json:"shortfalls,omitempty"`
	Scenarios  []scenarioRow  `json:"scenarios,omitempty"`
	Months     []monthRow     `json:"months,omitempty"`
	// MonthsNote says which months the overview left out.
	MonthsNote string            `json:"months_note,omitempty"`
	Totals     map[string]string `json:"totals,omitempty"`
	Insights   []insightRow      `json:"insights,omitempty"`
}

// insightRow is an insight as text in the configured language, plus its
// message key and arguments: the exact amounts ("1906.6 EUR"), months
// ("2027-03") and counts behind the text.
type insightRow struct {
	ID   string         `json:"id"`
	Tone budget.Tone    `json:"tone"`
	Text string         `json:"text"`
	Key  string         `json:"key"`
	Args map[string]any `json:"args,omitempty"`
}

func newInsightRow(in budget.Insight) insightRow {
	return insightRow{
		ID:   in.ID,
		Tone: in.Tone,
		Text: i18n.StripMarkdown(in.Text),
		Key:  in.Key,
		Args: flatArgs(in.Args),
	}
}

// flatArgs turns typed message arguments into plain values: exact money as
// "amount CODE", months as "YYYY-MM", counts as numbers, lists as lists.
func flatArgs(args map[string]i18n.Arg) map[string]any {
	if len(args) == 0 {
		return nil
	}
	out := make(map[string]any, len(args))
	for k, a := range args {
		out[k] = flatArg(a)
	}
	return out
}

func flatArg(a i18n.Arg) any {
	switch {
	case a.Money != nil:
		return a.Money.String() + " " + a.Money.Currency().Code
	case a.Month != "":
		return a.Month
	case a.Int != nil:
		return *a.Int
	case a.Number != "":
		return a.Number
	case a.Msg != nil:
		return flatMsg(*a.Msg)
	case a.List != nil:
		items := make([]any, len(a.List))
		for i, m := range a.List {
			items[i] = flatMsg(m)
		}
		return items
	}
	return a.Text
}

// flatMsg is a nested message's arguments, or its key when it has none.
func flatMsg(m i18n.Msg) any {
	if len(m.Args) == 0 {
		return m.Key
	}
	return flatArgs(m.Args)
}

type settingsRow struct {
	BaseCurrency   string                `json:"base_currency"`
	StartMonth     string                `json:"start_month"`
	Months         int                   `json:"months"`
	OverdraftStart string                `json:"overdraft_start,omitempty"`
	ExtraToDebtPct string                `json:"extra_to_debt_pct"`
	DebtStrategy   string                `json:"debt_strategy"`
	Currencies     []budget.CurrencyRate `json:"currencies"`
}

type itemRow struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
	// Base is one occurrence in base currency, transfer loss included.
	Base     string `json:"base"`
	Schedule string `json:"schedule"`
	// Pot: income goes straight into this pot / the cost is paid from it.
	Pot string `json:"pot,omitempty"`
}

type debtRow struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Currency      string `json:"currency"`
	Balance       string `json:"balance"`
	APRPct        string `json:"apr_pct"`
	MinPayment    string `json:"min_payment"`
	EndBalance    string `json:"end_balance"`
	TotalInterest string `json:"total_interest"`
	PaidOff       string `json:"paid_off"`
	// PayoffPlan says when and from where the debt is paid off in full, if set.
	PayoffPlan string `json:"payoff_plan,omitempty"`
}

type goalRow struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Target  string `json:"target"`
	Reached string `json:"reached"`
}

// potRow amounts are base currency.
type potRow struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Balance string `json:"balance"`
	Target  string `json:"target,omitempty"`
	Monthly string `json:"monthly,omitempty"`
	End     string `json:"end_balance"`
	Default bool   `json:"gets_leftover,omitempty"`
}

type shortfallRow struct {
	Month   string            `json:"month"`
	Short   string            `json:"short"`
	CoverBy map[string]string `json:"could_cover_from,omitempty"`
}

type scenarioRow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Ops         int    `json:"ops"`
}

// monthRow amounts are base currency.
type monthRow struct {
	Month       string `json:"month"`
	Income      string `json:"income"`
	Expenses    string `json:"expenses"`
	DebtPayment string `json:"debt_payments"`
	Payoff      string `json:"payoff,omitempty"`
	Left        string `json:"left"`
	FromSav     string `json:"from_savings,omitempty"`
	Overdraft   string `json:"overdraft,omitempty"`
	Saved       string `json:"saved"`
	Savings     string `json:"savings"`
}

// payoffPlan describes a debt's payoff: "Nov 2026 from General savings".
func payoffPlan(b budget.Budget, r *budget.Report, d budget.Debt) string {
	if d.PayoffMonth == nil {
		return ""
	}
	when := fmt.Sprintf("month %d, after this window", *d.PayoffMonth)
	if m := *d.PayoffMonth; m < len(r.Months) {
		when = r.Months[m].Label
	}
	from := "that month's money"
	isPayoffPot := func(p budget.Pot) bool { return p.ID == d.PayoffFrom }
	if i := slices.IndexFunc(b.Pots, isPayoffPot); i >= 0 {
		from = b.Pots[i].Name
	}
	return when + " from " + from
}

func summarize(v *app.View) Summary {
	s := summarizeReport(v.Budget, v.Report)
	s.ID = v.ID
	s.Revision = v.Revision
	return s
}

func summarizeReport(b budget.Budget, r *budget.Report) Summary {
	return Summary{
		Name:        b.Name,
		Note:        b.Description,
		Settings:    settingsRowFromBudget(b),
		Categories:  b.Categories,
		Items:       itemRows(b, r),
		Debts:       debtRows(b, r),
		Pots:        potRows(b, r),
		Withdrawals: b.Withdrawals,
		Shortfalls:  shortfallRows(r),
		Goals:       goalRows(r),
		Scenarios:   scenarioRows(b),
		Months:      monthRows(r),
		Totals:      totalsFromReport(r),
		Insights:    reportInsightRows(r),
	}
}

func settingsRowFromBudget(b budget.Budget) settingsRow {
	settings := settingsRow{
		BaseCurrency:   b.BaseCurrency,
		StartMonth:     b.StartMonth,
		Months:         b.Months,
		ExtraToDebtPct: b.ExtraToDebtPct.String(),
		DebtStrategy:   b.DebtStrategy,
		Currencies:     b.Currencies,
	}
	if !b.OverdraftStart.IsZero() {
		settings.OverdraftStart = b.OverdraftStart.String()
	}
	return settings
}

func itemRows(b budget.Budget, r *budget.Report) []itemRow {
	var rows []itemRow
	for _, line := range r.Items {
		isLine := func(item budget.Item) bool { return item.ID == line.ID }
		pot := ""
		if i := slices.IndexFunc(b.Items, isLine); i >= 0 {
			pot = b.Items[i].Pot
		}
		rows = append(rows, itemRow{
			ID:       line.ID,
			Name:     line.Name,
			Category: line.Category,
			Amount:   line.Amount.String(),
			Currency: line.Amount.Currency().Code,
			Base:     line.PerOccurrence.String(),
			Schedule: line.ScheduleText,
			Pot:      pot,
		})
	}
	return rows
}

func debtRows(b budget.Budget, r *budget.Report) []debtRow {
	var rows []debtRow
	for i, summary := range r.Debts {
		budgetDebt := b.Debts[i]
		rows = append(rows, debtRow{
			ID:            summary.ID,
			Name:          summary.Name,
			Currency:      budgetDebt.Currency,
			Balance:       budgetDebt.Balance.String(),
			APRPct:        budgetDebt.APRPct.String(),
			MinPayment:    budgetDebt.MinPayment.String(),
			EndBalance:    summary.EndBalance.String(),
			TotalInterest: summary.TotalInterest.String(),
			PaidOff:       paidOffText(summary),
			PayoffPlan:    payoffPlan(b, r, budgetDebt),
		})
	}
	return rows
}

func paidOffText(summary budget.DebtSummary) string {
	switch {
	case summary.PaidOffLabel != "":
		return summary.PaidOffLabel
	case summary.StartBalance.IsZero():
		return "nothing owed"
	}
	return notWithinWindow
}

func potRows(b budget.Budget, r *budget.Report) []potRow {
	var rows []potRow
	for i, summary := range r.Pots {
		budgetPot := b.Pots[i]
		row := potRow{
			ID:      summary.ID,
			Name:    summary.Name,
			Kind:    summary.Kind,
			Balance: summary.Start.String(),
			End:     summary.End.String(),
			Default: summary.Default,
		}
		if !budgetPot.Target.IsZero() {
			row.Target = budgetPot.Target.String()
		}
		if !budgetPot.Monthly.IsZero() {
			row.Monthly = budgetPot.Monthly.String()
		}
		rows = append(rows, row)
	}
	return rows
}

func shortfallRows(r *budget.Report) []shortfallRow {
	var rows []shortfallRow
	for _, shortfall := range r.Shortfalls {
		row := shortfallRow{Month: shortfall.Label, Short: shortfall.Amount.String()}
		if len(shortfall.Options) > 0 {
			row.CoverBy = map[string]string{}
			for _, option := range shortfall.Options {
				pot := option.Name + " (" + option.Kind + ")"
				row.CoverBy[pot] = option.Available.String() + " available"
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func goalRows(r *budget.Report) []goalRow {
	var rows []goalRow
	for _, status := range r.Goals {
		rows = append(rows, goalRow{
			ID:      status.ID,
			Name:    status.Name,
			Target:  status.Target.String(),
			Reached: reachedText(status),
		})
	}
	return rows
}

func reachedText(status budget.GoalStatus) string {
	reached := notWithinWindow
	switch {
	case status.AlreadyMet:
		reached = string(goalAlreadyMet)
	case status.ReachedLabel != "":
		reached = status.ReachedLabel
	case status.NeededPerMonth != nil:
		reached += "; needs " + status.NeededPerMonth.String() + "/month"
	}
	return reached
}

func scenarioRows(b budget.Budget) []scenarioRow {
	var rows []scenarioRow
	for _, scenario := range b.Scenarios {
		rows = append(rows, scenarioRow{
			ID:          scenario.ID,
			Name:        scenario.Name,
			Description: scenario.Description,
			Ops:         len(scenario.Ops),
		})
	}
	return rows
}

func monthRows(r *budget.Report) []monthRow {
	var rows []monthRow
	for _, month := range r.Months {
		row := monthRow{
			Month:       month.Key,
			Income:      month.Income.String(),
			Expenses:    month.Expenses.String(),
			DebtPayment: month.DebtPayments.String(),
			Left:        month.Left.String(),
			Saved:       month.Saved.String(),
			Savings:     month.Savings.String(),
		}
		if month.FromSavings.IsPositive() {
			row.FromSav = month.FromSavings.String()
		}
		if month.Payoff.IsPositive() {
			row.Payoff = month.Payoff.String()
		}
		if month.Overdraft.IsPositive() {
			row.Overdraft = month.Overdraft.String()
		}
		rows = append(rows, row)
	}
	return rows
}

func totalsFromReport(r *budget.Report) map[string]string {
	totals := r.Totals
	return map[string]string{
		"income":        totals.Income.String(),
		"expenses":      totals.Expenses.String(),
		"debt_payments": totals.DebtPayments.String(),
		"saved":         totals.Saved.String(),
		"end_savings":   totals.EndSavings.String(),
		"interest":      totals.TotalInterest.String(),
	}
}

func reportInsightRows(r *budget.Report) []insightRow {
	var rows []insightRow
	for _, insight := range r.Insights {
		rows = append(rows, newInsightRow(insight))
	}
	return rows
}

type outcomeRow struct {
	Debts            map[string]string `json:"debts,omitempty"`
	DeltaEndSavings  string            `json:"delta_end_savings_vs_baseline,omitempty"`
	EndSavings       *string           `json:"end_savings,omitempty"`
	Error            string            `json:"error,omitempty"`
	FirstMonthSaved  *string           `json:"first_month_saved,omitempty"`
	Goals            map[string]string `json:"goals,omitzero"`
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	NegativeSavingIn string            `json:"savings_negative_in,omitempty"`
	TotalInterest    *string           `json:"total_interest,omitempty"`
}

func outcomeRows(outcomes []budget.Outcome) []outcomeRow {
	rows := make([]outcomeRow, len(outcomes))
	for i, outcome := range outcomes {
		rows[i] = newOutcomeRow(outcome)
	}
	return rows
}

func newOutcomeRow(outcome budget.Outcome) outcomeRow {
	if outcome.Error != "" {
		return outcomeRow{ID: outcome.ID, Name: outcome.Name, Error: outcome.Error}
	}
	row := outcomeRow{
		Debts:            outcomeDebts(outcome),
		EndSavings:       moneyText(outcome.EndSavings),
		FirstMonthSaved:  moneyText(outcome.FirstMonthSaved),
		Goals:            outcomeGoals(outcome),
		ID:               outcome.ID,
		Name:             outcome.Name,
		NegativeSavingIn: outcome.NegativeSavingIn,
		TotalInterest:    moneyText(outcome.TotalInterest),
	}
	if outcome.DeltaEndSavings != nil {
		row.DeltaEndSavings = outcome.DeltaEndSavings.String()
	}
	return row
}

func moneyText(m *money.Money) *string {
	text := ""
	if m != nil {
		text = m.String()
	}
	return &text
}

func outcomeGoals(outcome budget.Outcome) map[string]string {
	goals := map[string]string{}
	for _, goal := range outcome.Goals {
		switch {
		case goal.AlreadyMet:
			goals[goal.Name] = string(goalAlreadyMet)
		case goal.ReachedLabel != "":
			goals[goal.Name] = goal.ReachedLabel
		default:
			goals[goal.Name] = "not reached"
		}
	}
	return goals
}

func outcomeDebts(outcome budget.Outcome) map[string]string {
	debts := map[string]string{}
	for _, debt := range outcome.Debts {
		if debt.PaidOffLabel != "" {
			debts[debt.Name] = "paid off " + debt.PaidOffLabel
		} else {
			endBalance := debt.EndBalance
			debts[debt.Name] = "ends at " + endBalance.String() + " " + endBalance.Currency().Code
		}
	}
	return debts
}
