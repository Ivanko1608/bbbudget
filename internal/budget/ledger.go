package budget

import (
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

// NewTransaction checks a transaction input against the budget and works out
// its value in the base currency. The returned transaction still needs its ID,
// Batch, Source and LoggedAt. path prefixes issue paths ("transactions[2]").
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
