package budget

import (
	"fmt"
	"math/big"
	"strings"
	"time"

	"bbbudget/internal/i18n"
	"bbbudget/internal/money"
)

// Transaction is money that actually came in or went out, as opposed to a
// planned Line. It is stored in the budget's ledger, not in the budget.
type Transaction struct {
	ID string `json:"id"`
	// Date is the day it happened, "YYYY-MM-DD".
	Date string `json:"date"`
	// Amount is positive, in Currency. The category says whether it is
	// income or spending.
	Amount   money.Decimal `json:"amount"`
	Currency string        `json:"currency"`
	Category string        `json:"category"`
	// Line optionally ties it to the planned line it pays.
	Line string `json:"line,omitempty"`
	Note string `json:"note,omitempty"`
	// ExternalID identifies it in its source (a bank statement row), so
	// importing the same statement twice logs nothing twice.
	ExternalID string `json:"external_id,omitempty"`
	// Base is its value in the base currency, fixed when it was logged with
	// that day's rate and the plan's rounding (costs up, income down), so
	// past spending doesn't move when rates do.
	Base money.Decimal `json:"base"`
	// Batch groups the transactions logged together, so an import can be
	// taken back in one go.
	Batch    string    `json:"batch"`
	Source   string    `json:"source,omitempty"`
	LoggedAt time.Time `json:"logged_at"`
}

// TransactionInput is a transaction to log: Base, Batch, Source and
// LoggedAt are filled in.
type TransactionInput struct {
	Date       string        `json:"date"`
	Amount     money.Decimal `json:"amount"`
	Currency   string        `json:"currency,omitempty"`
	Category   string        `json:"category"`
	Line       string        `json:"line,omitempty"`
	Note       string        `json:"note,omitempty"`
	ExternalID string        `json:"external_id,omitempty"`
}

// Limits on a ledger.
const (
	MaxTransactions = 100_000
	maxNote         = 300
	maxExternalID   = 200
)

// Value checks a transaction against the budget and works out its base
// value. The returned transaction still needs its ID, Batch, Source and
// LoggedAt. path prefixes issue paths ("transactions[2]").
func (p *Plan) Value(in TransactionInput, path string) (Transaction, []Issue) {
	var c checker
	t := Transaction{Date: strings.TrimSpace(in.Date), Amount: in.Amount, Currency: strings.ToUpper(strings.TrimSpace(in.Currency)),
		Category: strings.TrimSpace(in.Category), Line: strings.TrimSpace(in.Line), Note: strings.TrimSpace(in.Note), ExternalID: strings.TrimSpace(in.ExternalID)}
	if t.Currency == "" {
		t.Currency = p.Base.Code
	}
	if _, err := time.Parse("2006-01-02", t.Date); err != nil {
		c.add(path+".date", "issue.date_format", args{"value": i18n.Text(t.Date)})
	}
	cat, ok := p.Categories[t.Category]
	if !ok {
		c.add(path+".category", "issue.unknown_category", args{"id": i18n.Text(t.Category)})
	}
	if t.Line != "" {
		if i, err := find("line", t.Line, ids(p.Doc.Lines, lineKey)); err != nil {
			c.add(path+".line", "issue.error", errArg(err))
		} else {
			t.Line = p.Doc.Lines[i].ID
		}
	}
	if len(t.Note) > maxNote {
		c.add(path+".note", "issue.too_long", args{"max": intArg(maxNote)})
	}
	if len(t.ExternalID) > maxExternalID {
		c.add(path+".external_id", "issue.too_long", args{"max": intArg(maxExternalID)})
	}
	cur, ok := p.Currency(t.Currency)
	if !ok {
		c.add(path+".currency", "issue.no_rate", args{"code": i18n.Text(t.Currency)})
		return t, c.issues
	}
	m := c.amount(path+".amount", t.Amount, cur, false)
	if len(c.issues) == 0 && !m.IsPositive() {
		c.add(path+".amount", "issue.positive", nil)
	}
	if len(c.issues) > 0 {
		return t, c.issues
	}
	if cat.Kind == KindIncome {
		t.Base = p.IncomeValue(m, roundReceive).Decimal()
	} else {
		t.Base = p.PayCost(m, roundPay).Decimal()
	}
	t.Amount = m.Decimal()
	return t, nil
}

// CategoryStatus is one category's plan and actuals for a month, in the
// base currency.
type CategoryStatus struct {
	Category string      `json:"category"`
	Label    string      `json:"label"`
	Kind     string      `json:"kind"`
	Planned  money.Money `json:"planned"`
	Actual   money.Money `json:"actual"`
	// Remaining is Planned - Actual: what is left to spend (or still to
	// come in, for income). Negative means over plan.
	Remaining money.Money `json:"remaining"`
	// Projected is spending extrapolated to the end of the month at the
	// pace so far; set for spending in the current month.
	Projected *money.Money `json:"projected,omitempty"`
	// State: "ok", "over" (spent more than planned), "pace" (on course to
	// overspend), "unplanned" (spent with nothing planned), "short" (income
	// below plan once the month is over).
	State string `json:"state"`
}

// MonthStatus is plan versus actual for one month.
type MonthStatus struct {
	Month string `json:"month"`
	// Days and Elapsed are the month's length and the days gone by
	// (Elapsed = Days for a past month, 0 for a future one).
	Days         int              `json:"days"`
	Elapsed      int              `json:"elapsed"`
	Categories   []CategoryStatus `json:"categories"`
	Planned      money.Money      `json:"planned_spending"`
	Spent        money.Money      `json:"spent"`
	IncomePlan   money.Money      `json:"planned_income"`
	Income       money.Money      `json:"income"`
	Transactions int              `json:"transactions"`
	// Flags are the things worth saying, as messages (see Localize).
	Flags []Insight `json:"flags"`
}

// Status compares a month ("YYYY-MM") of the plan with the transactions
// logged in it. today decides how far into the month we are.
func (p *Plan) Status(month string, txs []Transaction, today time.Time) (*MonthStatus, error) {
	start, err := time.Parse("2006-01", month)
	if err != nil {
		return nil, fmt.Errorf("month must be YYYY-MM, got %q", month)
	}
	idx := (start.Year()-p.Start.Year())*12 + int(start.Month()-p.Start.Month())
	if idx < 0 || idx >= p.Months {
		return nil, fmt.Errorf("%s is outside the budget's window (%s for %d months)", month, p.Doc.StartMonth, p.Months)
	}
	end := start.AddDate(0, 1, 0)
	// Empty lists, not null: clients iterate over them.
	st := &MonthStatus{Month: month, Days: int(end.Sub(start).Hours() / 24), Categories: []CategoryStatus{}, Flags: []Insight{}}
	day := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	switch {
	case !day.Before(end):
		st.Elapsed = st.Days
	case !day.Before(start):
		st.Elapsed = int(day.Sub(start).Hours()/24) + 1
	}

	zero := money.Zero(p.Base)
	st.Planned, st.Spent, st.IncomePlan, st.Income = zero, zero, zero, zero
	planned, actual := map[string]money.Money{}, map[string]money.Money{}
	add := func(m map[string]money.Money, k string, v money.Money) {
		if t, ok := m[k]; ok {
			m[k] = t.Add(v)
		} else {
			m[k] = v
		}
	}
	for _, l := range p.Lines {
		if !l.Schedule.ActiveIn(idx) {
			continue
		}
		if l.Income {
			add(planned, l.Category, p.IncomeValue(l.Amt, roundReceive))
		} else {
			add(planned, l.Category, p.PayCost(l.Amt, roundPay))
		}
	}
	for _, t := range txs {
		if !strings.HasPrefix(t.Date, month+"-") {
			continue
		}
		v, err := money.FromDecimal(t.Base, p.Base)
		if err != nil {
			return nil, err
		}
		add(actual, t.Category, v)
		st.Transactions++
	}

	for _, c := range p.Doc.Categories {
		pl, hasPlan := planned[c.ID]
		ac, hasActual := actual[c.ID]
		if !hasPlan && !hasActual {
			continue
		}
		if !hasPlan {
			pl = zero
		}
		if !hasActual {
			ac = zero
		}
		cs := CategoryStatus{Category: c.ID, Label: c.Label, Kind: c.Kind, Planned: pl, Actual: ac, Remaining: pl.Sub(ac), State: "ok"}
		name := i18n.Text(c.Label)
		if c.Kind == KindIncome {
			st.IncomePlan, st.Income = st.IncomePlan.Add(pl), st.Income.Add(ac)
			if st.Elapsed == st.Days && ac.Cmp(pl) < 0 {
				cs.State = "short"
				st.Flags = append(st.Flags, Insight{ID: "status_short_" + c.ID, Tone: ToneWarn, Msg: msg("status.short", args{"category": name, "actual": w(ac), "planned": w(pl)})})
			}
			st.Categories = append(st.Categories, cs)
			continue
		}
		st.Planned, st.Spent = st.Planned.Add(pl), st.Spent.Add(ac)
		if st.Elapsed > 0 && st.Elapsed < st.Days && ac.IsPositive() {
			proj := ac.Mul(big.NewRat(int64(st.Days), int64(st.Elapsed)), money.Ceil)
			cs.Projected = &proj
		}
		switch {
		case !hasPlan || !pl.IsPositive():
			cs.State = "unplanned"
			st.Flags = append(st.Flags, Insight{ID: "status_unplanned_" + c.ID, Tone: ToneWarn, Msg: msg("status.unplanned", args{"category": name, "actual": w(ac)})})
		case ac.Cmp(pl) > 0:
			cs.State = "over"
			st.Flags = append(st.Flags, Insight{ID: "status_over_" + c.ID, Tone: ToneWarn, Msg: msg("status.over", args{"category": name, "actual": w(ac), "planned": w(pl), "over": w(ac.Sub(pl))})})
		case cs.Projected != nil && cs.Projected.Cmp(pl.Mul(big.NewRat(11, 10), money.Floor)) > 0:
			cs.State = "pace"
			st.Flags = append(st.Flags, Insight{ID: "status_pace_" + c.ID, Tone: ToneInfo, Msg: msg("status.pace", args{"category": name, "projected": w(*cs.Projected), "planned": w(pl)})})
		}
		st.Categories = append(st.Categories, cs)
	}
	st.Localize(english())
	return st, nil
}

// Localize renders the flags in l's language.
func (s *MonthStatus) Localize(l *i18n.Localizer) {
	for i := range s.Flags {
		s.Flags[i].Text = l.T(s.Flags[i].Msg)
	}
}
