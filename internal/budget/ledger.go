package budget

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"bbbudget/internal/i18n"
	"bbbudget/internal/money"
)

// Transaction is money that actually came in or went out, as opposed to a
// planned Item. It is stored in the budget's ledger, not in the budget.
type Transaction struct {
	ID string `json:"id"`
	// Date is the day it happened, "YYYY-MM-DD".
	Date string `json:"date"`
	// Amount is positive, in Currency. The category says whether it is
	// income or spending.
	Amount   money.Decimal `json:"amount"`
	Currency string        `json:"currency"`
	Category string        `json:"category"`
	// Item optionally ties it to the planned item it pays.
	Item string `json:"item,omitempty"`
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
	Item       string        `json:"item,omitempty"`
	Note       string        `json:"note,omitempty"`
	ExternalID string        `json:"external_id,omitempty"`
}

// Limits on a ledger.
const (
	MaxTransactions = 100_000
	maxNote         = 300
	maxExternalID   = 200
)

// trimmed is the input as it is stored: without surrounding spaces, and
// with the currency code in upper case. Callers (people, models, pasted
// statements) send "eur" or " Food", but the category, the currency and
// the external id are looked up and compared exactly.
func (in TransactionInput) trimmed() TransactionInput {
	in.Date = strings.TrimSpace(in.Date)
	in.Currency = strings.ToUpper(strings.TrimSpace(in.Currency))
	in.Category = strings.TrimSpace(in.Category)
	in.Item = strings.TrimSpace(in.Item)
	in.Note = strings.TrimSpace(in.Note)
	in.ExternalID = strings.TrimSpace(in.ExternalID)
	return in
}

// NewTransaction checks a transaction input against the budget and works out its value in
// the base currency. The returned transaction still needs its ID, Batch,
// Source and LoggedAt. path prefixes issue paths ("transactions[2]").
func (p *Plan) NewTransaction(in TransactionInput, path string) (Transaction, []Issue) {
	in = in.trimmed()
	if in.Currency == "" {
		in.Currency = p.Base.Code
	}
	t := Transaction{
		Date:       in.Date,
		Amount:     in.Amount,
		Currency:   in.Currency,
		Category:   in.Category,
		Item:       in.Item,
		Note:       in.Note,
		ExternalID: in.ExternalID,
	}
	var c checker
	t.Item = p.checkTransaction(&c, t, path)
	amount := p.checkAmount(&c, t, path)
	if len(c.issues) > 0 {
		return t, c.issues
	}
	t.Amount = amount.Decimal()
	t.Base = p.baseValue(amount, p.Categories[t.Category]).Decimal()
	return t, nil
}

// checkTransaction checks everything but the amount, and returns the id of
// the item t names.
func (p *Plan) checkTransaction(c *checker, t Transaction, path string) (itemID string) {
	if _, err := time.Parse(time.DateOnly, t.Date); err != nil {
		c.add(path+".date", "issue.date_format", args{"value": i18n.Text(t.Date)})
	}
	if _, known := p.Categories[t.Category]; !known {
		c.add(path+".category", "issue.unknown_category", args{"id": i18n.Text(t.Category)})
	}
	if len(t.Note) > maxNote {
		c.add(path+".note", "issue.too_long", args{"max": i18n.Int(maxNote)})
	}
	if len(t.ExternalID) > maxExternalID {
		c.add(path+".external_id", "issue.too_long", args{"max": i18n.Int(maxExternalID)})
	}
	if t.Item == "" {
		return ""
	}
	i, err := find("item", t.Item, ids(p.Doc.Items, itemKey))
	if err != nil {
		c.add(path+".item", "issue.error", errArg(err))
		return t.Item
	}
	return p.Doc.Items[i].ID
}

// checkAmount checks the currency and the amount, which must be positive.
func (p *Plan) checkAmount(c *checker, t Transaction, path string) money.Money {
	currency, ok := p.Currency(t.Currency)
	if !ok {
		c.add(path+".currency", "issue.no_rate", args{"code": i18n.Text(t.Currency)})
		return money.Money{}
	}
	issuesBefore := len(c.issues)
	amount := c.amount(path+".amount", t.Amount, currency, false)
	amountIsValid := len(c.issues) == issuesBefore
	if amountIsValid && !amount.IsPositive() {
		c.add(path+".amount", "issue.positive", nil)
	}
	return amount
}

// baseValue is what an amount is worth in the base currency, rounded the
// way the plan rounds: income down, costs up.
func (p *Plan) baseValue(amount money.Money, category Category) money.Money {
	if category.Kind == KindIncome {
		return p.IncomeValue(amount, roundReceive)
	}
	return p.PayCost(amount, roundPay)
}

// SpendingState says how a category's month compares with the plan.
type SpendingState string

const (
	StateOK SpendingState = "ok"
	// StateOver is spending above plan.
	StateOver SpendingState = "over"
	// StateProjectedOver is spending on course to go more than
	// projectionTolerance over plan.
	StateProjectedOver SpendingState = "projected_over"
	// StateUnplanned is spending with nothing planned.
	StateUnplanned SpendingState = "unplanned"
	// StateShort is income below plan once the month is over.
	StateShort SpendingState = "short"
)

// projectionTolerance is how far over plan the projected spending may go before it
// is flagged: 10%.
var projectionTolerance = big.NewRat(11, 10)

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
	Projected *money.Money  `json:"projected,omitempty"`
	State     SpendingState `json:"state"`
}

// MonthStatus is plan versus actual for one month.
type MonthStatus struct {
	Month string `json:"month"`
	// DaysInMonth and DaysElapsed are the month's length and the days gone by
	// (DaysElapsed = DaysInMonth for a past month, 0 for a future one).
	DaysInMonth      int              `json:"days_in_month"`
	DaysElapsed      int              `json:"days_elapsed"`
	Categories       []CategoryStatus `json:"categories"`
	PlannedSpending  money.Money      `json:"planned_spending"`
	Spent            money.Money      `json:"spent"`
	PlannedIncome    money.Money      `json:"planned_income"`
	Income           money.Money      `json:"income"`
	TransactionCount int              `json:"transaction_count"`
	// Flags are the things worth saying, as messages (see Localize).
	Flags []Insight `json:"flags"`
}

// MonthStatus compares a month ("YYYY-MM") of the plan with the transactions
// logged in it. today decides how far into the month we are.
func (p *Plan) MonthStatus(
	month string, transactions []Transaction, today time.Time,
) (*MonthStatus, error) {
	index, start, err := p.monthIndex(month)
	if err != nil {
		return nil, err
	}
	planned := p.plannedTotals(index)
	actual, count, err := actualTotals(transactions, month, p.Base)
	if err != nil {
		return nil, err
	}
	status := newMonthStatus(month, start, today, p.Base)
	status.TransactionCount = count
	for _, c := range p.Doc.Categories {
		if !planned.has(c.ID) && !actual.has(c.ID) {
			continue
		}
		if c.Kind == KindIncome {
			status.addIncome(c, planned.get(c.ID), actual.get(c.ID))
		} else {
			status.addSpending(c, planned.get(c.ID), actual.get(c.ID))
		}
	}
	status.Localize(english())
	return status, nil
}

// ErrOutsideWindow means a month is before the budget's start or after its end.
var ErrOutsideWindow = errors.New("outside the budget's window")

// monthIndex finds a "YYYY-MM" month in the plan's window, and its first day.
func (p *Plan) monthIndex(month string) (int, time.Time, error) {
	start, err := time.Parse("2006-01", month)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("month must be YYYY-MM, got %q", month)
	}
	index := (start.Year()-p.Start.Year())*12 + int(start.Month()-p.Start.Month())
	if index < 0 || index >= p.Months {
		return 0, time.Time{}, fmt.Errorf("%s is %w (%s for %d months)",
			month, ErrOutsideWindow, p.Doc.StartMonth, p.Months)
	}
	return index, start, nil
}

func newMonthStatus(month string, start, today time.Time, base money.Currency) *MonthStatus {
	days := start.AddDate(0, 1, -1).Day()
	zero := money.Zero(base)
	return &MonthStatus{
		Month:       month,
		DaysInMonth: days,
		DaysElapsed: elapsedDays(start, days, today),
		// Empty lists, not null: clients iterate over them.
		Categories:      []CategoryStatus{},
		Flags:           []Insight{},
		PlannedSpending: zero,
		Spent:           zero,
		PlannedIncome:   zero,
		Income:          zero,
	}
}

// elapsedDays counts the days of the month up to and including today.
func elapsedDays(start time.Time, days int, today time.Time) int {
	day := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	switch {
	case !day.Before(end):
		return days
	case day.Before(start):
		return 0
	}
	return day.Day()
}

// categoryTotals adds up amounts per category.
type categoryTotals struct {
	base       money.Currency
	byCategory map[string]money.Money
}

func newCategoryTotals(base money.Currency) categoryTotals {
	return categoryTotals{base: base, byCategory: map[string]money.Money{}}
}

func (s categoryTotals) add(category string, amount money.Money) {
	s.byCategory[category] = s.get(category).Add(amount)
}

func (s categoryTotals) has(category string) bool {
	_, ok := s.byCategory[category]
	return ok
}

// get is the sum for a category, zero if it has none.
func (s categoryTotals) get(category string) money.Money {
	if sum, ok := s.byCategory[category]; ok {
		return sum
	}
	return money.Zero(s.base)
}

func (p *Plan) plannedTotals(month int) categoryTotals {
	planned := newCategoryTotals(p.Base)
	for _, item := range p.Items {
		if !item.Schedule.ActiveIn(month) {
			continue
		}
		if item.Income {
			planned.add(item.Category, p.IncomeValue(item.Amt, roundReceive))
		} else {
			planned.add(item.Category, p.PayCost(item.Amt, roundPay))
		}
	}
	return planned
}

// actualTotals sums the base values of a month's transactions, and
// counts them.
func actualTotals(
	transactions []Transaction, month string, base money.Currency,
) (categoryTotals, int, error) {
	actual := newCategoryTotals(base)
	count := 0
	for _, t := range transactions {
		if !strings.HasPrefix(t.Date, month+"-") {
			continue
		}
		value, err := money.FromDecimal(t.Base, base)
		if err != nil {
			return categoryTotals{}, 0, err
		}
		actual.add(t.Category, value)
		count++
	}
	return actual, count, nil
}

func (s *MonthStatus) addIncome(c Category, planned, actual money.Money) {
	s.PlannedIncome = s.PlannedIncome.Add(planned)
	s.Income = s.Income.Add(actual)
	row := newCategoryStatus(c, planned, actual)
	monthEnded := s.DaysElapsed == s.DaysInMonth
	if monthEnded && actual.Cmp(planned) < 0 {
		row.State = StateShort
		s.addFlag(c, row)
	}
	s.Categories = append(s.Categories, row)
}

func (s *MonthStatus) addSpending(c Category, planned, actual money.Money) {
	s.PlannedSpending = s.PlannedSpending.Add(planned)
	s.Spent = s.Spent.Add(actual)
	row := newCategoryStatus(c, planned, actual)
	row.Projected = s.projectSpending(actual)
	row.State = spendingState(planned, actual, row.Projected)
	if row.State != StateOK {
		s.addFlag(c, row)
	}
	s.Categories = append(s.Categories, row)
}

func newCategoryStatus(c Category, planned, actual money.Money) CategoryStatus {
	return CategoryStatus{
		Category:  c.ID,
		Label:     c.Label,
		Kind:      c.Kind,
		Planned:   planned,
		Actual:    actual,
		Remaining: planned.Sub(actual),
		State:     StateOK,
	}
}

// projectSpending is spending extrapolated to the end of the month, or nil when
// the month hasn't started, is over, or has no spending yet.
func (s *MonthStatus) projectSpending(actual money.Money) *money.Money {
	monthUnderway := s.DaysElapsed > 0 && s.DaysElapsed < s.DaysInMonth
	if !monthUnderway || !actual.IsPositive() {
		return nil
	}
	atThisPace := actual.Mul(big.NewRat(int64(s.DaysInMonth), int64(s.DaysElapsed)), money.Ceil)
	return &atThisPace
}

func spendingState(planned, actual money.Money, projected *money.Money) SpendingState {
	switch {
	case !planned.IsPositive() && actual.IsPositive():
		return StateUnplanned
	case actual.Cmp(planned) > 0:
		return StateOver
	case projected != nil && projected.Cmp(planned.Mul(projectionTolerance, money.Floor)) > 0:
		return StateProjectedOver
	}
	return StateOK
}

// addFlag adds the insight that says what row's state means.
func (s *MonthStatus) addFlag(c Category, row CategoryStatus) {
	a := args{
		"category": i18n.Text(c.Label),
		"actual":   i18n.Money(row.Actual),
		"planned":  i18n.Money(row.Planned),
	}
	tone := ToneWarn
	switch row.State {
	case StateOver:
		a["over"] = i18n.Money(row.Actual.Sub(row.Planned))
	case StateProjectedOver:
		a["projected"] = i18n.Money(*row.Projected)
		tone = ToneInfo
	}
	id := "status_" + string(row.State) + "_" + c.ID
	s.Flags = append(s.Flags, Insight{ID: id, Tone: tone, Msg: msg("status."+string(row.State), a)})
}

// Localize renders the flags in l's language.
func (s *MonthStatus) Localize(l *i18n.Localizer) {
	for i := range s.Flags {
		s.Flags[i].Text = l.Render(s.Flags[i].Msg)
	}
}
