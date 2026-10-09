package mcpserver

import (
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
		SavingsStart   string                `json:"savings_start"`
		ExtraToDebtPct string                `json:"extra_to_debt_pct"`
		DebtStrategy   string                `json:"debt_strategy"`
		Currencies     []budget.CurrencyRate `json:"currencies"`
	} `json:"settings"`
	Categories []budget.Category `json:"categories"`
	Lines      []lineRow         `json:"lines"`
	Debts      []debtRow         `json:"debts,omitempty"`
	Goals      []goalRow         `json:"goals,omitempty"`
	Scenarios  []scenarioRow     `json:"scenarios,omitempty"`
	Months     []monthRow        `json:"months"`
	Totals     map[string]string `json:"totals"`
	Insights   []insightRow      `json:"insights"`
}

// insightRow is an insight as text in the configured language, plus its
// message key and arguments: the exact amounts ("1906.6 EUR"), months
// ("2027-03") and counts behind the text.
type insightRow struct {
	ID   string         `json:"id"`
	Tone string         `json:"tone"`
	Text string         `json:"text"`
	Key  string         `json:"key"`
	Args map[string]any `json:"args,omitempty"`
}

func newInsightRow(in budget.Insight) insightRow {
	return insightRow{in.ID, in.Tone, plain(in.Text), in.Key, flatArgs(in.Args)}
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
	case a.List != nil:
		items := make([]any, len(a.List))
		for i, m := range a.List {
			if len(m.Args) == 0 {
				items[i] = m.Key
			} else {
				items[i] = flatArgs(m.Args)
			}
		}
		return items
	}
	return a.Text
}

type lineRow struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
	// Base is one occurrence in base currency, transfer loss included.
	Base     string `json:"base"`
	Schedule string `json:"schedule"`
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
}

type goalRow struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Target  string `json:"target"`
	Reached string `json:"reached"`
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
	Left        string `json:"left"`
	Saved       string `json:"saved"`
	Savings     string `json:"savings"`
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
	s.Settings.SavingsStart = b.SavingsStart.String()
	s.Settings.ExtraToDebtPct = b.ExtraToDebtPct.String()
	s.Settings.DebtStrategy = b.DebtStrategy
	s.Settings.Currencies = b.Currencies
	s.Categories = b.Categories
	for _, l := range r.Lines {
		s.Lines = append(s.Lines, lineRow{l.ID, l.Name, l.Category, l.Amount.String(), l.Amount.Currency().Code, l.PerOccurrence.String(), l.ScheduleText})
	}
	for i, d := range r.Debts {
		doc := b.Debts[i]
		paid := "not within the window"
		if d.PaidOffLabel != "" {
			paid = d.PaidOffLabel
		} else if d.StartBalance.IsZero() {
			paid = "nothing owed"
		}
		s.Debts = append(s.Debts, debtRow{d.ID, d.Name, doc.Currency, doc.Balance.String(), doc.APRPct.String(), doc.MinPayment.String(), d.EndBalance.String(), d.TotalInterest.String(), paid})
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
		s.Months = append(s.Months, monthRow{m.Key, m.Income.String(), m.Expenses.String(),
			m.DebtPayments.String(), m.Left.String(), m.Saved.String(), m.Savings.String()})
	}
	t := r.Totals
	s.Totals = map[string]string{
		"income": t.Income.String(), "expenses": t.Expenses.String(), "debt_payments": t.DebtPayments.String(),
		"saved": t.Saved.String(), "end_savings": t.EndSavings.String(), "interest": t.TotalInterest.String(),
	}
	for _, in := range r.Insights {
		s.Insights = append(s.Insights, newInsightRow(in))
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
