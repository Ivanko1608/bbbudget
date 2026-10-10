package budget

import (
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"strings"
	"time"

	"bbbudget/internal/money"
)

// Bounds that keep every intermediate sum far inside int64 minor units.
const (
	MaxMonths = 600
	MaxItems  = 2000
	// MaxAmountMajor caps any single amount (10^12 major units).
	MaxAmountMajor = 1_000_000_000_000
	// MaxAmountMinor caps it in minor units too, which binds for currencies
	// with many decimals (10^15 satoshi is 10 million BTC).
	MaxAmountMinor = 1_000_000_000_000_000
)

// Issue is one validation problem, addressed by a JSON-ish path.
type Issue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// ValidationError lists every problem found, not just the first.
type ValidationError struct{ Issues []Issue }

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Issues))
	for i, is := range e.Issues {
		parts[i] = is.Path + ": " + is.Message
	}
	return "invalid budget: " + strings.Join(parts, "; ")
}

// Plan is a validated budget with exact money values, ready to project.
type Plan struct {
	Doc         Budget
	Base        money.Currency
	Start       time.Time // first day of the first month, UTC
	Months      int
	ExtraPct    money.Decimal
	Strategy    string
	Categories  map[string]Category
	Items       []PlanItem
	Debts       []PlanDebt
	Pots        []PlanPot
	Withdrawals []PlanWithdrawal
	// OverdraftStart is owed on the current account at the start.
	OverdraftStart money.Money
	// DefaultPot is the index of the pot that receives what no rule assigns:
	// the first general pot, else the last pot.
	DefaultPot int

	rates map[string]fx
}

type fx struct {
	cur     money.Currency
	perBase *big.Rat
	feeMul  *big.Rat // 1 + fee/100
	keepMul *big.Rat // 1 - cash-out fee/100: the share of income that arrives
}

type PlanItem struct {
	Item
	Amt    money.Money
	Income bool
	// PotIdx indexes Plan.Pots for an item routed through a pot, else -1.
	PotIdx int
}

type PlanDebt struct {
	Debt
	Bal, Min money.Money
	// MonthlyRate is APR/1200, exact.
	MonthlyRate *big.Rat
	// PotIdx indexes Plan.Pots for a payoff paid from a pot, else -1.
	PotIdx int
}

type PlanPot struct {
	Pot
	Bal, Target, Monthly money.Money
}

type PlanWithdrawal struct {
	Withdrawal
	Amt money.Money
	// PotIdx indexes Plan.Pots.
	PotIdx int
}

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

type checker struct{ issues []Issue }

func (c *checker) add(path, format string, a ...any) {
	c.issues = append(c.issues, Issue{path, fmt.Sprintf(format, a...)})
}

func (c *checker) amount(path string, d money.Decimal, cur money.Currency, allowNeg bool) money.Money {
	m, err := money.FromDecimal(d, cur)
	if err != nil {
		c.add(path, "%v", err)
		return money.Zero(cur)
	}
	if !allowNeg && m.IsNegative() {
		c.add(path, "must not be negative")
	}
	if d.Rat().Abs(d.Rat()).Cmp(big.NewRat(MaxAmountMajor, 1)) > 0 || m.Minor() > MaxAmountMinor || m.Minor() < -MaxAmountMinor {
		c.add(path, "is unreasonably large (max %d)", MaxAmountMajor)
		return money.Zero(cur)
	}
	return m
}

func (c *checker) pct(path string, d money.Decimal, max int64) {
	if d.Sign() < 0 || d.Rat().Cmp(big.NewRat(max, 1)) > 0 {
		c.add(path, "must be between 0 and %d", max)
	}
}

func (c *checker) id(path, id string, seen map[string]bool) {
	if !idRe.MatchString(id) {
		c.add(path, "id %q must be lowercase letters, digits, '-' or '_'", id)
	} else if seen[id] {
		c.add(path, "duplicate id %q", id)
	}
	seen[id] = true
}

// Compile validates a budget and converts it to a Plan. All problems are
// reported together in a *ValidationError.
func Compile(b Budget) (*Plan, error) {
	c := &checker{}
	p := &Plan{Doc: b, Categories: map[string]Category{}, rates: map[string]fx{}, Strategy: b.DebtStrategy, ExtraPct: b.ExtraToDebtPct}

	if strings.TrimSpace(b.Name) == "" {
		c.add("name", "is required")
	}
	base, err := money.Lookup(b.BaseCurrency)
	if err != nil {
		c.add("base_currency", "%v", err)
		base = money.MustLookup("EUR")
	}
	if base.Crypto {
		c.add("base_currency", "%s is a cryptocurrency; plan in a fiat currency and add %s under currencies", base.Code, base.Code)
	}
	p.Base = base
	one := big.NewRat(1, 1)
	p.rates[base.Code] = fx{base, one, one, one}

	if t, err := time.Parse("2006-01", b.StartMonth); err != nil {
		c.add("start_month", "must be YYYY-MM, got %q", b.StartMonth)
	} else {
		p.Start = t
	}
	if b.Months < 1 || b.Months > MaxMonths {
		c.add("months", "must be between 1 and %d", MaxMonths)
	}
	p.Months = b.Months
	p.OverdraftStart = c.amount("overdraft_start", b.OverdraftStart, base, false)
	c.pct("extra_to_debt_pct", b.ExtraToDebtPct, 100)
	switch b.DebtStrategy {
	case StrategyAvalanche, StrategySnowball, StrategyListed:
	case "":
		p.Strategy = StrategyAvalanche
	default:
		c.add("debt_strategy", "must be avalanche, snowball or listed")
	}

	for i, r := range b.Currencies {
		path := fmt.Sprintf("currencies[%d]", i)
		cur, err := money.Lookup(r.Code)
		switch {
		case err != nil:
			c.add(path+".code", "%v", err)
			continue
		case r.Code == base.Code:
			c.add(path+".code", "%s is the base currency; it needs no rate", r.Code)
			continue
		case p.rates[r.Code].cur.Code != "":
			c.add(path+".code", "duplicate currency %s", r.Code)
			continue
		}
		if r.PerBase.Sign() <= 0 {
			c.add(path+".per_base", "must be positive")
			continue
		}
		c.pct(path+".transfer_fee_pct", r.TransferFeePct, 100)
		if r.CashOutFeePct.Sign() < 0 || r.CashOutFeePct.Rat().Cmp(big.NewRat(100, 1)) >= 0 {
			c.add(path+".cash_out_fee_pct", "must be at least 0 and below 100")
		}
		if !slices.Contains(RateSources, r.Source) && r.Source != "" {
			c.add(path+".source", "must be one of %s", strings.Join(RateSources, ", "))
		}
		fee := new(big.Rat).Quo(r.TransferFeePct.Rat(), big.NewRat(100, 1))
		keep := new(big.Rat).Quo(r.CashOutFeePct.Rat(), big.NewRat(100, 1))
		p.rates[r.Code] = fx{cur, r.PerBase.Rat(), fee.Add(fee, one), keep.Sub(one, keep)}
	}
	currency := func(path, code string) (money.Currency, bool) {
		r, ok := p.rates[code]
		if !ok {
			c.add(path, "currency %q has no rate; add it to currencies", code)
		}
		return r.cur, ok
	}

	seen := map[string]bool{}
	for i, cat := range b.Categories {
		path := fmt.Sprintf("categories[%d]", i)
		c.id(path+".id", cat.ID, seen)
		if cat.Kind != KindIncome && cat.Kind != KindExpense {
			c.add(path+".kind", "must be income or expense")
		}
		if strings.TrimSpace(cat.Label) == "" {
			c.add(path+".label", "is required")
		}
		p.Categories[cat.ID] = cat
	}

	if len(b.Items) > MaxItems {
		c.add("items", "at most %d items", MaxItems)
	}
	seen = map[string]bool{}
	for i, l := range b.Items {
		path := fmt.Sprintf("items[%d]", i)
		c.id(path+".id", l.ID, seen)
		cat, ok := p.Categories[l.Category]
		if !ok {
			c.add(path+".category", "unknown category %q", l.Category)
		}
		if s := l.Schedule; s.Start < 0 || s.Every < 0 || (s.End != nil && *s.End < s.Start) {
			c.add(path+".schedule", "needs start >= 0, every >= 0 and end >= start")
		}
		cur, ok := currency(path+".currency", l.Currency)
		if !ok {
			continue
		}
		p.Items = append(p.Items, PlanItem{Item: l, Amt: c.amount(path+".amount", l.Amount, cur, false), Income: cat.Kind == KindIncome, PotIdx: -1})
	}

	seen = map[string]bool{}
	for i, d := range b.Debts {
		path := fmt.Sprintf("debts[%d]", i)
		c.id(path+".id", d.ID, seen)
		c.pct(path+".apr_pct", d.APRPct, 1000)
		cur, ok := currency(path+".currency", d.Currency)
		if !ok {
			continue
		}
		p.Debts = append(p.Debts, PlanDebt{
			Debt:        d,
			Bal:         c.amount(path+".balance", d.Balance, cur, false),
			Min:         c.amount(path+".min_payment", d.MinPayment, cur, false),
			MonthlyRate: new(big.Rat).Quo(d.APRPct.Rat(), big.NewRat(1200, 1)),
			PotIdx:      -1,
		})
		if d.PayoffMonth != nil && (*d.PayoffMonth < 0 || *d.PayoffMonth >= MaxMonths) {
			c.add(path+".payoff_month", "must be between 0 and %d", MaxMonths-1)
		}
		if d.PayoffFrom != "" && d.PayoffMonth == nil {
			c.add(path+".payoff_from", "needs a payoff_month")
		}
	}

	seen = map[string]bool{}
	potIdx := map[string]int{}
	p.DefaultPot = -1
	if len(b.Pots) == 0 {
		c.add("pots", "add at least one savings pot: money left over each month goes there")
	}
	for i, pt := range b.Pots {
		path := fmt.Sprintf("pots[%d]", i)
		c.id(path+".id", pt.ID, seen)
		if strings.TrimSpace(pt.Name) == "" {
			c.add(path+".name", "is required")
		}
		if !slices.Contains(PotKinds, pt.Kind) {
			c.add(path+".kind", "must be one of %s", strings.Join(PotKinds, ", "))
		}
		if pt.Kind == PotGeneral && p.DefaultPot < 0 {
			p.DefaultPot = i
		}
		potIdx[pt.ID] = i
		p.Pots = append(p.Pots, PlanPot{Pot: pt,
			Bal:     c.amount(path+".balance", pt.Balance, base, false),
			Target:  c.amount(path+".target", pt.Target, base, false),
			Monthly: c.amount(path+".monthly", pt.Monthly, base, false)})
	}
	if p.DefaultPot < 0 {
		p.DefaultPot = len(p.Pots) - 1
	}
	for i := range p.Items {
		if ref := p.Items[i].Pot; ref != "" {
			k, ok := potIdx[ref]
			if !ok {
				c.add(fmt.Sprintf("items[%d].pot", indexWhere(b.Items, func(l Item) bool { return l.ID == p.Items[i].ID })), "unknown pot %q", ref)
				continue
			}
			p.Items[i].PotIdx = k
		}
	}
	for i := range p.Debts {
		if ref := p.Debts[i].PayoffFrom; ref != "" {
			k, ok := potIdx[ref]
			if !ok {
				c.add(fmt.Sprintf("debts[%d].payoff_from", indexWhere(b.Debts, func(d Debt) bool { return d.ID == p.Debts[i].ID })), "unknown pot %q", ref)
				continue
			}
			p.Debts[i].PotIdx = k
		}
	}

	seen = map[string]bool{}
	for i, w := range b.Withdrawals {
		path := fmt.Sprintf("withdrawals[%d]", i)
		c.id(path+".id", w.ID, seen)
		k, ok := potIdx[w.Pot]
		if !ok {
			c.add(path+".pot", "unknown pot %q", w.Pot)
		}
		if s := w.Schedule; s.Start < 0 || s.Every < 0 || (s.End != nil && *s.End < s.Start) {
			c.add(path+".schedule", "needs start >= 0, every >= 0 and end >= start")
		}
		amt := c.amount(path+".amount", w.Amount, base, false)
		if !amt.IsPositive() {
			c.add(path+".amount", "must be more than zero")
		}
		p.Withdrawals = append(p.Withdrawals, PlanWithdrawal{Withdrawal: w, Amt: amt, PotIdx: k})
	}

	seen = map[string]bool{}
	for i, s := range b.Scenarios {
		c.id(fmt.Sprintf("scenarios[%d].id", i), s.ID, seen)
	}

	if len(c.issues) > 0 {
		return nil, &ValidationError{c.issues}
	}
	return p, nil
}

// Currency returns a configured currency (base or foreign).
func (p *Plan) Currency(code string) (money.Currency, bool) {
	r, ok := p.rates[code]
	return r.cur, ok
}

// ToBase converts at the mid rate, without transfer loss.
func (p *Plan) ToBase(m money.Money, mode money.RoundingMode) money.Money {
	r := p.rates[m.Currency().Code]
	return money.FromRat(new(big.Rat).Quo(m.Rat(), r.perBase), p.Base, mode)
}

// IncomeValue is what income m (in its own currency) is worth in base
// currency once converted, after the currency's cash-out loss.
func (p *Plan) IncomeValue(m money.Money, mode money.RoundingMode) money.Money {
	r := p.rates[m.Currency().Code]
	v := new(big.Rat).Quo(m.Rat(), r.perBase)
	return money.FromRat(v.Mul(v, r.keepMul), p.Base, mode)
}

// CashOutPct returns the cash-out loss percent for a currency.
func (p *Plan) CashOutPct(code string) *big.Rat {
	v := new(big.Rat).Sub(big.NewRat(1, 1), p.rates[code].keepMul)
	return v.Mul(v, big.NewRat(100, 1))
}

// PayCost is what paying m (in its own currency) costs in base currency,
// including the transfer loss for that currency.
func (p *Plan) PayCost(m money.Money, mode money.RoundingMode) money.Money {
	r := p.rates[m.Currency().Code]
	v := new(big.Rat).Quo(m.Rat(), r.perBase)
	return money.FromRat(v.Mul(v, r.feeMul), p.Base, mode)
}

// Receivable is how much of currency `code` arrives when spending base
// amount b, after transfer loss.
func (p *Plan) Receivable(b money.Money, code string, mode money.RoundingMode) money.Money {
	r := p.rates[code]
	v := new(big.Rat).Mul(b.Rat(), r.perBase)
	return money.FromRat(v.Quo(v, r.feeMul), r.cur, mode)
}

// FeePct returns the transfer loss percent for a currency.
func (p *Plan) FeePct(code string) *big.Rat {
	r := p.rates[code]
	v := new(big.Rat).Sub(r.feeMul, big.NewRat(1, 1))
	return v.Mul(v, big.NewRat(100, 1))
}

// WithRate returns a copy of the plan with one currency's rate replaced.
func (p *Plan) WithRate(code string, perBase *big.Rat) *Plan {
	q := *p
	q.rates = make(map[string]fx, len(p.rates))
	for k, v := range p.rates {
		q.rates[k] = v
	}
	r := q.rates[code]
	r.perBase = perBase
	q.rates[code] = r
	return &q
}

// MonthLabel returns "Oct 2026" for month index i.
func (p *Plan) MonthLabel(i int) string { return p.Start.AddDate(0, i, 0).Format("Jan 2006") }

// MonthKey returns "2026-10" for month index i.
func (p *Plan) MonthKey(i int) string { return p.Start.AddDate(0, i, 0).Format("2006-01") }
