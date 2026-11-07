// Package budget is the planning domain: the budget document, its validation,
// the month-by-month projection engine, insight rules and scenarios.
//
// A Budget is the editable document (what is stored and sent over the wire;
// amounts are exact decimal strings). Compile validates it into a Plan, whose
// amounts are money.Money. Only Plans are ever projected.
package budget

import (
	"slices"
	"strings"

	"bbbudget/internal/money"
)

// Category kinds.
const (
	KindIncome  = "income"
	KindExpense = "expense"
)

// Debt payoff strategies for the extra payment.
const (
	StrategyAvalanche = "avalanche" // highest APR first: least interest paid
	StrategySnowball  = "snowball"  // smallest balance first: fastest first win
	StrategyListed    = "listed"    // in the order the debts are listed
)

// Strategies lists every valid Budget.DebtStrategy.
var Strategies = []string{StrategyAvalanche, StrategySnowball, StrategyListed}

// Budget is the stored, editable document.
type Budget struct {
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	BaseCurrency string `json:"base_currency"`
	// StartMonth is the first projected month, "YYYY-MM".
	StartMonth string `json:"start_month"`
	// Months is the projection length.
	Months int `json:"months"`
	// OverdraftStart is money already owed on the current account at the
	// start (base currency, not negative). Later surpluses repay it first.
	OverdraftStart money.Decimal `json:"overdraft_start,omitzero"`
	// Currencies lists every non-base currency used, with its rate and transfer loss.
	Currencies []CurrencyRate `json:"currencies"`
	Categories []Category     `json:"categories"`
	Items      []Item         `json:"items"`
	Debts      []Debt         `json:"debts"`
	// ExtraToDebtPct is the percent of each month's positive leftover sent to debts.
	ExtraToDebtPct money.Decimal `json:"extra_to_debt_pct"`
	DebtStrategy   string        `json:"debt_strategy"`
	// Pots hold savings. Savings are balances, never income: a short month is
	// never covered from a pot unless a Withdrawal says so.
	Pots []Pot `json:"pots"`
	// Withdrawals are planned transfers from a pot into a month's spending.
	Withdrawals []Withdrawal `json:"withdrawals"`
	Scenarios   []Scenario   `json:"scenarios"`
}

// CurrencyRate configures one foreign currency.
type CurrencyRate struct {
	Code string `json:"code"`
	// PerBase is units of this currency per 1 unit of base currency (EUR→USD: "1.08").
	PerBase money.Decimal `json:"per_base"`
	// TransferFeePct is the percent lost when paying in this currency (fees, spread).
	// It is added on top of every outgoing payment in this currency.
	TransferFeePct money.Decimal `json:"transfer_fee_pct"`
	// CashOutFeePct is the percent lost when income in this currency is
	// converted to the base currency (exchange fees, spread, withdrawal fees).
	// Typical for crypto income. Zero when omitted.
	CashOutFeePct money.Decimal `json:"cash_out_fee_pct,omitzero"`
	// Source says where PerBase comes from: "manual" (or empty) for a rate you
	// type, otherwise a rate provider that refreshing fetches it from (see RateSources).
	Source RateSource `json:"source,omitempty"`
	// RateDate and RateFrom record the last fetched quote: its date (YYYY-MM-DD)
	// and the provider that supplied it. Informational; cleared by a manual rate.
	RateDate string     `json:"rate_date,omitempty"`
	RateFrom RateSource `json:"rate_from,omitempty"`
}

// RateSource names where a currency's exchange rate comes from.
type RateSource string

// RateSourceAuto tries the official ECB rate first and falls back to a provider
// that also covers crypto and the currencies ECB doesn't quote.
const (
	RateSourceManual      RateSource = "manual"
	RateSourceAuto        RateSource = "auto"
	RateSourceECB         RateSource = "ecb"
	RateSourceCurrencyAPI RateSource = "currency-api"
	RateSourceCoinGecko   RateSource = "coingecko"
)

// RateSources lists every valid CurrencyRate.Source.
var RateSources = []RateSource{
	RateSourceManual,
	RateSourceAuto,
	RateSourceECB,
	RateSourceCurrencyAPI,
	RateSourceCoinGecko,
}

// FormatRateSources lists RateSources as comma-separated text.
func FormatRateSources() string {
	names := make([]string, len(RateSources))
	for i, source := range RateSources {
		names[i] = string(source)
	}
	return strings.Join(names, ", ")
}

// IsManual reports whether the rate is typed by hand rather than fetched.
func (r CurrencyRate) IsManual() bool { return r.Source == "" || r.Source == RateSourceManual }

// GetCurrencyRateByCode returns the budget's currency with the given code.
func (b Budget) GetCurrencyRateByCode(code string) (CurrencyRate, bool) {
	i := slices.IndexFunc(b.Currencies, func(c CurrencyRate) bool { return c.Code == code })
	if i < 0 {
		return CurrencyRate{}, false
	}
	return b.Currencies[i], true
}

type Category struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Kind is "income" or "expense".
	Kind  string `json:"kind"`
	Color string `json:"color,omitempty"`
}

// Item is one source of income or one cost.
type Item struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Amount   money.Decimal `json:"amount"`
	Currency string        `json:"currency"`
	Category string        `json:"category"`
	Schedule Schedule      `json:"schedule"`
	// Pot routes the item through a savings pot instead of the month's
	// budget: income goes straight into the pot (it is not spendable that
	// month and no share of it goes to debts); a cost is paid from the pot,
	// as far as the pot holds, and only the rest comes out of the month.
	Pot string `json:"pot,omitempty"`
}

// Schedule says in which projection months (0-based) an item applies.
type Schedule struct {
	// Start is the first month index the item applies.
	Start int `json:"start"`
	// Every is the period in months: 1 = monthly, 3 = quarterly, 12 = yearly, 0 = once (in Start).
	Every int `json:"every"`
	// End is the last month index (inclusive); nil = no end.
	End *int `json:"end,omitempty"`
}

// Monthly is the common schedule: every month from the start.
func Monthly() Schedule { return Schedule{Every: 1} }

// Once applies only in month m.
func Once(m int) Schedule { return Schedule{Start: m} }

// IsOneOff reports whether the schedule applies in a single month.
func (s Schedule) IsOneOff() bool { return s.Every == 0 }

// ActiveIn reports whether the schedule applies in month m.
func (s Schedule) ActiveIn(m int) bool {
	if m < s.Start || (s.End != nil && m > *s.End) {
		return false
	}
	if s.Every == 0 {
		return m == s.Start
	}
	return (m-s.Start)%s.Every == 0
}

// Debt is a balance that accrues interest and receives a minimum payment.
type Debt struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Currency string        `json:"currency"`
	Balance  money.Decimal `json:"balance"`
	// APRPct is the yearly interest rate in percent. Interest compounds monthly at APR/12.
	APRPct money.Decimal `json:"apr_pct"`
	// MinPayment is paid each month (in the debt's currency) until the balance is zero.
	MinPayment money.Decimal `json:"min_payment"`
	// PayoffMonth, when set, pays off the whole balance (that month's interest
	// included) in that month (0-based index from start_month).
	PayoffMonth *int `json:"payoff_month,omitempty"`
	// PayoffFrom is the pot that pays it off, after income routed to pots has
	// landed that month. "" pays it from the month's own money. A pot that holds
	// less pays what it can; the rest stays owed, with its minimum payment.
	PayoffFrom string `json:"payoff_from,omitempty"`
}

// PaysOffIn reports whether the debt is set to be paid off in full in month m.
func (d Debt) PaysOffIn(m int) bool { return d.PayoffMonth != nil && *d.PayoffMonth == m }

// Pot kinds. They decide how a pot is offered when a month is short.
const (
	PotGeneral   = "general"   // unassigned savings: offered first
	PotSinking   = "sinking"   // saving ahead for a known cost: offered, delays that cost
	PotEmergency = "emergency" // for emergencies: offered last, as a last resort
)

// PotKinds lists every valid Pot.Kind, in the order pots are offered to cover a short month.
var PotKinds = []string{PotGeneral, PotSinking, PotEmergency}

// Pot is a savings pot in base currency. Each month's leftover goes, in
// order: fixed Monthly contributions, the share to debts, filling pots that
// have a Target (in list order), then the first general pot.
type Pot struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Balance is what the pot holds at the start.
	Balance money.Decimal `json:"balance"`
	// Target is optional: the pot stops receiving money once it is reached.
	Target money.Decimal `json:"target,omitzero"`
	// Monthly is an optional fixed contribution, paid when the month has room.
	Monthly money.Decimal `json:"monthly,omitzero"`
}

// Withdrawal takes money out of a pot to spend it: shown as "from savings",
// never as income. It never takes more than the pot holds.
type Withdrawal struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Pot      string        `json:"pot"`
	Amount   money.Decimal `json:"amount"`
	Schedule Schedule      `json:"schedule"`
}

// Goal is the old form of a savings target (before pots). It is still read,
// and becomes a pot with that target.
type Goal struct {
	ID     string        `json:"id"`
	Name   string        `json:"name"`
	Target money.Decimal `json:"target"`
}

// Scenario is a named set of edits applied on top of the budget for comparison.
type Scenario struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Ops         []Op   `json:"ops"`
}
