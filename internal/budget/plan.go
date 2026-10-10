package budget

import (
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"bbbudget/internal/i18n"
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

// Issue is one validation problem, addressed by a JSON-ish path. Message is
// the English text; Key and Args let a transport render it in the reader's
// language (Issue.Localized).
type Issue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
	i18n.Msg
}

// Localized returns the issue with Message in l's language.
func (i Issue) Localized(l *i18n.Localizer) Issue {
	if i.Key != "" {
		i.Message = l.T(i.Msg)
	}
	return i
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
	Doc          Budget
	Base         money.Currency
	Start        time.Time // first day of the first month, UTC
	Months       int
	SavingsStart money.Money
	ExtraPct     money.Decimal
	Strategy     string
	Categories   map[string]Category
	Items        []PlanItem
	Debts        []PlanDebt
	Goals        []PlanGoal

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
}

type PlanDebt struct {
	Debt
	Bal, Min money.Money
	// MonthlyRate is APR/1200, exact.
	MonthlyRate *big.Rat
}

type PlanGoal struct {
	Goal
	Amt money.Money
}

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

type checker struct{ issues []Issue }

var english = sync.OnceValue(func() *i18n.Localizer { return i18n.For(i18n.Default) })

func (c *checker) add(path, key string, a args) { c.issues = append(c.issues, issue(path, key, a)) }

func issue(path, key string, a args) Issue {
	m := i18n.Msg{Key: key, Args: a}
	return Issue{Path: path, Message: english().T(m), Msg: m}
}

func errArg(err error) args   { return args{"error": i18n.Text(err.Error())} }
func intArg(n int64) i18n.Arg { return i18n.Text(fmt.Sprint(n)) } // limits, ungrouped as before

func (c *checker) amount(path string, d money.Decimal, cur money.Currency, allowNeg bool) money.Money {
	m, err := money.FromDecimal(d, cur)
	if err != nil {
		c.add(path, "issue.error", errArg(err))
		return money.Zero(cur)
	}
	if !allowNeg && m.IsNegative() {
		c.add(path, "issue.negative", nil)
	}
	if d.Rat().Abs(d.Rat()).Cmp(big.NewRat(MaxAmountMajor, 1)) > 0 || m.Minor() > MaxAmountMinor || m.Minor() < -MaxAmountMinor {
		c.add(path, "issue.too_large", args{"max": intArg(MaxAmountMajor)})
		return money.Zero(cur)
	}
	return m
}

func (c *checker) pct(path string, d money.Decimal, max int64) {
	if d.Sign() < 0 || d.Rat().Cmp(big.NewRat(max, 1)) > 0 {
		c.add(path, "issue.between", args{"min": intArg(0), "max": intArg(max)})
	}
}

func (c *checker) id(path, id string, seen map[string]bool) {
	if !idRe.MatchString(id) {
		c.add(path, "issue.id_format", args{"id": i18n.Text(id)})
	} else if seen[id] {
		c.add(path, "issue.duplicate_id", args{"id": i18n.Text(id)})
	}
	seen[id] = true
}

// Compile validates a budget and converts it to a Plan. All problems are
// reported together in a *ValidationError.
func Compile(b Budget) (*Plan, error) {
	c := &checker{}
	p := &Plan{Doc: b, Categories: map[string]Category{}, rates: map[string]fx{}, Strategy: b.DebtStrategy, ExtraPct: b.ExtraToDebtPct}

	if strings.TrimSpace(b.Name) == "" {
		c.add("name", "issue.required", nil)
	}
	base, err := money.Lookup(b.BaseCurrency)
	if err != nil {
		c.add("base_currency", "issue.error", errArg(err))
		base = money.MustLookup("EUR")
	}
	if base.Crypto {
		c.add("base_currency", "issue.base_crypto", args{"code": i18n.Text(base.Code)})
	}
	p.Base = base
	one := big.NewRat(1, 1)
	p.rates[base.Code] = fx{base, one, one, one}

	if t, err := time.Parse("2006-01", b.StartMonth); err != nil {
		c.add("start_month", "issue.month_format", args{"value": i18n.Text(b.StartMonth)})
	} else {
		p.Start = t
	}
	if b.Months < 1 || b.Months > MaxMonths {
		c.add("months", "issue.between", args{"min": intArg(1), "max": intArg(MaxMonths)})
	}
	p.Months = b.Months
	p.SavingsStart = c.amount("savings_start", b.SavingsStart, base, true)
	c.pct("extra_to_debt_pct", b.ExtraToDebtPct, 100)
	switch b.DebtStrategy {
	case StrategyAvalanche, StrategySnowball, StrategyListed:
	case "":
		p.Strategy = StrategyAvalanche
	default:
		c.add("debt_strategy", "issue.strategy", nil)
	}

	for i, r := range b.Currencies {
		path := fmt.Sprintf("currencies[%d]", i)
		cur, err := money.Lookup(r.Code)
		switch {
		case err != nil:
			c.add(path+".code", "issue.error", errArg(err))
			continue
		case r.Code == base.Code:
			c.add(path+".code", "issue.is_base", args{"code": i18n.Text(r.Code)})
			continue
		case p.rates[r.Code].cur.Code != "":
			c.add(path+".code", "issue.duplicate_currency", args{"code": i18n.Text(r.Code)})
			continue
		}
		if r.PerBase.Sign() <= 0 {
			c.add(path+".per_base", "issue.positive", nil)
			continue
		}
		c.pct(path+".transfer_fee_pct", r.TransferFeePct, 100)
		if r.CashOutFeePct.Sign() < 0 || r.CashOutFeePct.Rat().Cmp(big.NewRat(100, 1)) >= 0 {
			c.add(path+".cash_out_fee_pct", "issue.cash_out_range", nil)
		}
		if !slices.Contains(RateSources, r.Source) && r.Source != "" {
			c.add(path+".source", "issue.one_of", args{"values": i18n.Text(strings.Join(RateSources, ", "))})
		}
		fee := new(big.Rat).Quo(r.TransferFeePct.Rat(), big.NewRat(100, 1))
		keep := new(big.Rat).Quo(r.CashOutFeePct.Rat(), big.NewRat(100, 1))
		p.rates[r.Code] = fx{cur, r.PerBase.Rat(), fee.Add(fee, one), keep.Sub(one, keep)}
	}
	currency := func(path, code string) (money.Currency, bool) {
		r, ok := p.rates[code]
		if !ok {
			c.add(path, "issue.no_rate", args{"code": i18n.Text(code)})
		}
		return r.cur, ok
	}

	seen := map[string]bool{}
	for i, cat := range b.Categories {
		path := fmt.Sprintf("categories[%d]", i)
		c.id(path+".id", cat.ID, seen)
		if cat.Kind != KindIncome && cat.Kind != KindExpense {
			c.add(path+".kind", "issue.kind", nil)
		}
		if strings.TrimSpace(cat.Label) == "" {
			c.add(path+".label", "issue.required", nil)
		}
		p.Categories[cat.ID] = cat
	}

	if len(b.Items) > MaxItems {
		c.add("items", "issue.max_items", args{"max": intArg(MaxItems)})
	}
	seen = map[string]bool{}
	for i, l := range b.Items {
		path := fmt.Sprintf("items[%d]", i)
		c.id(path+".id", l.ID, seen)
		cat, ok := p.Categories[l.Category]
		if !ok {
			c.add(path+".category", "issue.unknown_category", args{"id": i18n.Text(l.Category)})
		}
		if s := l.Schedule; s.Start < 0 || s.Every < 0 || (s.End != nil && *s.End < s.Start) {
			c.add(path+".schedule", "issue.schedule", nil)
		}
		cur, ok := currency(path+".currency", l.Currency)
		if !ok {
			continue
		}
		p.Items = append(p.Items, PlanItem{Item: l, Amt: c.amount(path+".amount", l.Amount, cur, false), Income: cat.Kind == KindIncome})
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
		})
	}

	seen = map[string]bool{}
	for i, g := range b.Goals {
		path := fmt.Sprintf("goals[%d]", i)
		c.id(path+".id", g.ID, seen)
		p.Goals = append(p.Goals, PlanGoal{g, c.amount(path+".target", g.Target, base, false)})
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
