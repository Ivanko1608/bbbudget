package mcpserver

import (
	"fmt"
	"strings"

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
	Settings struct {
		BaseCurrency   string                `json:"base_currency"`
		StartMonth     string                `json:"start_month"`
		Months         int                   `json:"months"`
		OverdraftStart string                `json:"overdraft_start,omitempty"`
		ExtraToDebtPct string                `json:"extra_to_debt_pct"`
		DebtStrategy   string                `json:"debt_strategy"`
		Currencies     []budget.CurrencyRate `json:"currencies"`
	} `json:"settings"`
	Categories  []budget.Category   `json:"categories"`
	Items       []itemRow           `json:"items"`
	Debts       []debtRow           `json:"debts,omitempty"`
	Pots        []potRow            `json:"pots"`
	Withdrawals []budget.Withdrawal `json:"withdrawals,omitempty"`
	Goals       []goalRow           `json:"goals,omitempty"`
	// Shortfalls: months that fall short and the pots that could cover them.
	// Savings are never used automatically; suggest add_withdrawal.
	Shortfalls []shortfallRow    `json:"shortfalls,omitempty"`
	Scenarios  []scenarioRow     `json:"scenarios,omitempty"`
	Months     []monthRow        `json:"months"`
	Totals     map[string]string `json:"totals"`
	Insights   []insightRow      `json:"insights"`
}

// insightRow is an insight as text in the configured language, plus its
// message key and typed arguments: the exact amounts and months behind it.
type insightRow struct {
	ID   string              `json:"id"`
	Tone string              `json:"tone"`
	Text string              `json:"text"`
	Key  string              `json:"key"`
	Args map[string]i18n.Arg `json:"args,omitempty"`
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
	if i := indexWhere(b.Pots, func(p budget.Pot) bool { return p.ID == d.PayoffFrom }); i >= 0 {
		from = b.Pots[i].Name
	}
	return when + " from " + from
}

func plain(s string) string { return strings.ReplaceAll(s, "**", "") }

func summarize(v *app.View) Summary {
	s := summarizeReport(v.Budget, v.Report)
	s.ID, s.Revision = v.ID, v.Revision
	return s
}

func summarizeReport(b budget.Budget, r *budget.Report) Summary {
	var s Summary
	s.Name, s.Note = b.Name, b.Description
	s.Settings.BaseCurrency = b.BaseCurrency
	s.Settings.StartMonth = b.StartMonth
	s.Settings.Months = b.Months
	if !b.OverdraftStart.IsZero() {
		s.Settings.OverdraftStart = b.OverdraftStart.String()
	}
	s.Settings.ExtraToDebtPct = b.ExtraToDebtPct.String()
	s.Settings.DebtStrategy = b.DebtStrategy
	s.Settings.Currencies = b.Currencies
	s.Categories = b.Categories
	for _, l := range r.Items {
		pot := ""
		if i := indexWhere(b.Items, func(x budget.Item) bool { return x.ID == l.ID }); i >= 0 {
			pot = b.Items[i].Pot
		}
		s.Items = append(s.Items, itemRow{l.ID, l.Name, l.Category, l.Amount.String(), l.Amount.Currency().Code, l.PerOccurrence.String(), l.ScheduleText, pot})
	}
	for i, d := range r.Debts {
		doc := b.Debts[i]
		paid := "not within the window"
		if d.PaidOffLabel != "" {
			paid = d.PaidOffLabel
		} else if d.StartBalance.IsZero() {
			paid = "nothing owed"
		}
		s.Debts = append(s.Debts, debtRow{d.ID, d.Name, doc.Currency, doc.Balance.String(), doc.APRPct.String(), doc.MinPayment.String(), d.EndBalance.String(), d.TotalInterest.String(), paid, payoffPlan(b, r, doc)})
	}
	for i, p := range r.Pots {
		doc := b.Pots[i]
		row := potRow{ID: p.ID, Name: p.Name, Kind: p.Kind, Balance: p.Start.String(), End: p.End.String(), Default: p.Default}
		if !doc.Target.IsZero() {
			row.Target = doc.Target.String()
		}
		if !doc.Monthly.IsZero() {
			row.Monthly = doc.Monthly.String()
		}
		s.Pots = append(s.Pots, row)
	}
	s.Withdrawals = b.Withdrawals
	for _, sf := range r.Shortfalls {
		row := shortfallRow{Month: sf.Label, Short: sf.Amount.String()}
		if len(sf.Options) > 0 {
			row.CoverBy = map[string]string{}
			for _, o := range sf.Options {
				row.CoverBy[o.Name+" ("+o.Kind+")"] = o.Available.String() + " available"
			}
		}
		s.Shortfalls = append(s.Shortfalls, row)
	}
	for _, g := range r.Goals {
		reached := "not within the window"
		switch {
		case g.AlreadyMet:
			reached = "already met"
		case g.ReachedLabel != "":
			reached = g.ReachedLabel
		case g.NeededPerMonth != nil:
			reached += "; needs " + g.NeededPerMonth.String() + "/month"
		}
		s.Goals = append(s.Goals, goalRow{g.ID, g.Name, g.Target.String(), reached})
	}
	for _, sc := range b.Scenarios {
		s.Scenarios = append(s.Scenarios, scenarioRow{sc.ID, sc.Name, sc.Description, len(sc.Ops)})
	}
	for _, m := range r.Months {
		row := monthRow{Month: m.Key, Income: m.Income.String(), Expenses: m.Expenses.String(),
			DebtPayment: m.DebtPayments.String(), Left: m.Left.String(), Saved: m.Saved.String(), Savings: m.Savings.String()}
		if m.FromSavings.IsPositive() {
			row.FromSav = m.FromSavings.String()
		}
		if m.Payoff.IsPositive() {
			row.Payoff = m.Payoff.String()
		}
		if m.Overdraft.IsPositive() {
			row.Overdraft = m.Overdraft.String()
		}
		s.Months = append(s.Months, row)
	}
	t := r.Totals
	s.Totals = map[string]string{
		"income": t.Income.String(), "expenses": t.Expenses.String(), "debt_payments": t.DebtPayments.String(),
		"saved": t.Saved.String(), "end_savings": t.EndSavings.String(), "interest": t.TotalInterest.String(),
	}
	for _, in := range r.Insights {
		s.Insights = append(s.Insights, insightRow{in.ID, in.Tone, plain(in.Text), in.Key, in.Args})
	}
	return s
}

func outcomeRows(out []budget.Outcome) []map[string]any {
	str := func(m *money.Money) string {
		if m == nil {
			return ""
		}
		return m.String()
	}
	rows := make([]map[string]any, len(out))
	for i, o := range out {
		row := map[string]any{"id": o.ID, "name": o.Name}
		if o.Error != "" {
			row["error"] = o.Error
			rows[i] = row
			continue
		}
		row["first_month_saved"] = str(o.FirstMonthSaved)
		row["end_savings"] = str(o.EndSavings)
		if o.DeltaEndSavings != nil {
			row["delta_end_savings_vs_baseline"] = str(o.DeltaEndSavings)
		}
		row["total_interest"] = str(o.TotalInterest)
		goals := map[string]string{}
		for _, g := range o.Goals {
			switch {
			case g.AlreadyMet:
				goals[g.Name] = "already met"
			case g.ReachedLabel != "":
				goals[g.Name] = g.ReachedLabel
			default:
				goals[g.Name] = "not reached"
			}
		}
		row["goals"] = goals
		debts := map[string]string{}
		for _, d := range o.Debts {
			if d.PaidOffLabel != "" {
				debts[d.Name] = "paid off " + d.PaidOffLabel
			} else {
				debts[d.Name] = "ends at " + d.EndBalance.String() + " " + d.EndBalance.Currency().Code
			}
		}
		if len(debts) > 0 {
			row["debts"] = debts
		}
		if o.NegativeSavingIn != "" {
			row["savings_negative_in"] = o.NegativeSavingIn
		}
		rows[i] = row
	}
	return rows
}

func indexWhere[T any](s []T, f func(T) bool) int {
	for i, v := range s {
		if f(v) {
			return i
		}
	}
	return -1
}
