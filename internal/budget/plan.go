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
		i.Message = l.Render(i.Msg)
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

var english = sync.OnceValue(func() *i18n.Localizer { return i18n.For(i18n.Default) })

func (c *checker) add(path, key string, a args) { c.issues = append(c.issues, issue(path, key, a)) }

func issue(path, key string, a args) Issue {
	m := i18n.Msg{Key: key, Args: a}
	return Issue{Path: path, Message: english().Render(m), Msg: m}
}

func errArg(err error) args { return args{"error": i18n.Text(err.Error())} }

func (c *checker) amount(path string, d money.Decimal, cur money.Currency, allowNeg bool) money.Money {
	m, err := money.FromDecimal(d, cur)
	if err != nil {
		c.add(path, "issue.error", errArg(err))
		return money.Zero(cur)
	}
	if !allowNeg && m.IsNegative() {
		c.add(path, "issue.negative", nil)
	}
	if exceedsBounds(d, m) {
		c.add(path, "issue.too_large", args{"max": i18n.Int(MaxAmountMajor)})
		return money.Zero(cur)
	}
	return m
}

func exceedsBounds(d money.Decimal, m money.Money) bool {
	tooManyMajor := d.Abs().Cmp(money.DecimalFromInt(MaxAmountMajor)) > 0
	tooManyMinor := m.Minor() > MaxAmountMinor || m.Minor() < -MaxAmountMinor
	return tooManyMajor || tooManyMinor
}

func (c *checker) pct(path string, d money.Decimal, limit int64) {
	if d.Sign() < 0 || d.Rat().Cmp(big.NewRat(limit, 1)) > 0 {
		c.add(path, "issue.between", args{"min": i18n.Int(0), "max": i18n.Int(limit)})
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
	cm := newCompiler(b)
	cm.checkName()
	cm.compileBase()
	cm.compileStartMonth()
	cm.compileHorizon()
	cm.compileDebtStrategy()
	cm.compileRates()
	cm.compileCategories()
	cm.compileItems()
	cm.compileDebts()
	cm.compilePots()
	cm.linkItemPots()
	cm.linkDebtPots()
	cm.compileWithdrawals()
	cm.checkScenarios()

	if len(cm.issues) > 0 {
		return nil, &ValidationError{cm.issues}
	}
	return cm.plan, nil
}

type compiler struct {
	checker
	budget  Budget
	plan    *Plan
	potIdxs map[string]int
}

func newCompiler(b Budget) *compiler {
	plan := &Plan{
		Doc:        b,
		Categories: map[string]Category{},
		rates:      map[string]fx{},
		Strategy:   b.DebtStrategy,
		ExtraPct:   b.ExtraToDebtPct,
		DefaultPot: -1,
	}
	return &compiler{budget: b, plan: plan, potIdxs: map[string]int{}}
}

func (cm *compiler) checkName() {
	if strings.TrimSpace(cm.budget.Name) == "" {
		cm.add("name", "issue.required", nil)
	}
}

func (cm *compiler) compileBase() {
	base, err := money.Lookup(cm.budget.BaseCurrency)
	if err != nil {
		cm.add("base_currency", "issue.error", errArg(err))
		base = money.MustLookup("EUR")
	}
	if base.Crypto {
		cm.add("base_currency", "issue.base_crypto", args{"code": i18n.Text(base.Code)})
	}
	one := big.NewRat(1, 1)
	cm.plan.Base = base
	cm.plan.rates[base.Code] = fx{cur: base, perBase: one, feeMul: one, keepMul: one}
}

func (cm *compiler) compileStartMonth() {
	start, err := time.Parse("2006-01", cm.budget.StartMonth)
	if err != nil {
		cm.add("start_month", "issue.month_format", args{"value": i18n.Text(cm.budget.StartMonth)})
		return
	}
	cm.plan.Start = start
}

func (cm *compiler) compileHorizon() {
	b := cm.budget
	if b.Months < 1 || b.Months > MaxMonths {
		cm.add("months", "issue.between", args{"min": i18n.Int(1), "max": i18n.Int(MaxMonths)})
	}
	cm.plan.Months = b.Months
	cm.plan.OverdraftStart = cm.amount("overdraft_start", b.OverdraftStart, cm.plan.Base, false)
	cm.pct("extra_to_debt_pct", b.ExtraToDebtPct, 100)
}

func (cm *compiler) compileDebtStrategy() {
	switch cm.budget.DebtStrategy {
	case StrategyAvalanche, StrategySnowball, StrategyListed:
	case "":
		cm.plan.Strategy = StrategyAvalanche
	default:
		cm.add("debt_strategy", "issue.strategy", nil)
	}
}

func (cm *compiler) compileRates() {
	for i, rate := range cm.budget.Currencies {
		cm.compileRate(fmt.Sprintf("currencies[%d]", i), rate)
	}
}

func (cm *compiler) compileRate(path string, rate CurrencyRate) {
	cur, err := money.Lookup(rate.Code)
	switch {
	case err != nil:
		cm.add(path+".code", "issue.error", errArg(err))
		return
	case rate.Code == cm.plan.Base.Code:
		cm.add(path+".code", "issue.is_base", args{"code": i18n.Text(rate.Code)})
		return
	case cm.plan.rates[rate.Code].cur.Code != "":
		cm.add(path+".code", "issue.duplicate_currency", args{"code": i18n.Text(rate.Code)})
		return
	}
	if rate.PerBase.Sign() <= 0 {
		cm.add(path+".per_base", "issue.positive", nil)
		return
	}
	cm.checkRateFees(path, rate)
	cm.plan.rates[rate.Code] = fxFromRate(cur, rate)
}

func (cm *compiler) checkRateFees(path string, rate CurrencyRate) {
	cm.pct(path+".transfer_fee_pct", rate.TransferFeePct, 100)
	if rate.CashOutFeePct.Sign() < 0 || rate.CashOutFeePct.Rat().Cmp(big.NewRat(100, 1)) >= 0 {
		cm.add(path+".cash_out_fee_pct", "issue.cash_out_range", nil)
	}
	if rate.Source != "" && !slices.Contains(RateSources, rate.Source) {
		values := i18n.Text(strings.Join(RateSources, ", "))
		cm.add(path+".source", "issue.one_of", args{"values": values})
	}
}

func fxFromRate(cur money.Currency, rate CurrencyRate) fx {
	one := big.NewRat(1, 1)
	hundred := big.NewRat(100, 1)
	fee := new(big.Rat).Quo(rate.TransferFeePct.Rat(), hundred)
	cashOutFee := new(big.Rat).Quo(rate.CashOutFeePct.Rat(), hundred)
	return fx{
		cur:     cur,
		perBase: rate.PerBase.Rat(),
		feeMul:  fee.Add(fee, one),
		keepMul: cashOutFee.Sub(one, cashOutFee),
	}
}

func (cm *compiler) currency(path, code string) (money.Currency, bool) {
	rate, ok := cm.plan.rates[code]
	if !ok {
		cm.add(path, "issue.no_rate", args{"code": i18n.Text(code)})
	}
	return rate.cur, ok
}

func (cm *compiler) compileCategories() {
	seen := map[string]bool{}
	for i, cat := range cm.budget.Categories {
		path := fmt.Sprintf("categories[%d]", i)
		cm.id(path+".id", cat.ID, seen)
		if cat.Kind != KindIncome && cat.Kind != KindExpense {
			cm.add(path+".kind", "issue.kind", nil)
		}
		if strings.TrimSpace(cat.Label) == "" {
			cm.add(path+".label", "issue.required", nil)
		}
		cm.plan.Categories[cat.ID] = cat
	}
}

func (cm *compiler) compileItems() {
	if len(cm.budget.Items) > MaxItems {
		cm.add("items", "issue.max_items", args{"max": i18n.Int(MaxItems)})
	}
	seen := map[string]bool{}
	for i, item := range cm.budget.Items {
		path := fmt.Sprintf("items[%d]", i)
		cm.id(path+".id", item.ID, seen)
		cm.compileItem(path, item)
	}
}

func (cm *compiler) compileItem(path string, item Item) {
	cat, ok := cm.plan.Categories[item.Category]
	if !ok {
		cm.add(path+".category", "issue.unknown_category", args{"id": i18n.Text(item.Category)})
	}
	cm.checkSchedule(path+".schedule", item.Schedule)
	cur, ok := cm.currency(path+".currency", item.Currency)
	if !ok {
		return
	}
	cm.plan.Items = append(cm.plan.Items, PlanItem{
		Item:   item,
		Amt:    cm.amount(path+".amount", item.Amount, cur, false),
		Income: cat.Kind == KindIncome,
		PotIdx: -1,
	})
}

func (cm *compiler) checkSchedule(path string, s Schedule) {
	if s.Start < 0 || s.Every < 0 || (s.End != nil && *s.End < s.Start) {
		cm.add(path, "issue.schedule", nil)
	}
}

func (cm *compiler) compileDebts() {
	seen := map[string]bool{}
	for i, debt := range cm.budget.Debts {
		path := fmt.Sprintf("debts[%d]", i)
		cm.id(path+".id", debt.ID, seen)
		cm.pct(path+".apr_pct", debt.APRPct, 1000)
		cm.compileDebt(path, debt)
	}
}

func (cm *compiler) compileDebt(path string, debt Debt) {
	cur, ok := cm.currency(path+".currency", debt.Currency)
	if !ok {
		return
	}
	cm.plan.Debts = append(cm.plan.Debts, PlanDebt{
		Debt:        debt,
		Bal:         cm.amount(path+".balance", debt.Balance, cur, false),
		Min:         cm.amount(path+".min_payment", debt.MinPayment, cur, false),
		MonthlyRate: new(big.Rat).Quo(debt.APRPct.Rat(), big.NewRat(1200, 1)),
		PotIdx:      -1,
	})
	cm.checkPayoff(path, debt)
}

func (cm *compiler) checkPayoff(path string, debt Debt) {
	if debt.PayoffMonth != nil && (*debt.PayoffMonth < 0 || *debt.PayoffMonth >= MaxMonths) {
		bounds := args{"min": i18n.Int(0), "max": i18n.Int(MaxMonths - 1)}
		cm.add(path+".payoff_month", "issue.between", bounds)
	}
	if debt.PayoffFrom != "" && debt.PayoffMonth == nil {
		cm.add(path+".payoff_from", "issue.needs_payoff_month", nil)
	}
}

func (cm *compiler) compilePots() {
	if len(cm.budget.Pots) == 0 {
		cm.add("pots", "issue.no_pots", nil)
	}
	seen := map[string]bool{}
	for i, pot := range cm.budget.Pots {
		path := fmt.Sprintf("pots[%d]", i)
		cm.id(path+".id", pot.ID, seen)
		cm.compilePot(path, i, pot)
	}
	if cm.plan.DefaultPot < 0 {
		cm.plan.DefaultPot = len(cm.plan.Pots) - 1
	}
}

func (cm *compiler) compilePot(path string, index int, pot Pot) {
	if strings.TrimSpace(pot.Name) == "" {
		cm.add(path+".name", "issue.required", nil)
	}
	if !slices.Contains(PotKinds, pot.Kind) {
		cm.add(path+".kind", "issue.one_of", args{"values": i18n.Text(strings.Join(PotKinds, ", "))})
	}
	if pot.Kind == PotGeneral && cm.plan.DefaultPot < 0 {
		cm.plan.DefaultPot = index
	}
	cm.potIdxs[pot.ID] = index
	base := cm.plan.Base
	cm.plan.Pots = append(cm.plan.Pots, PlanPot{
		Pot:     pot,
		Bal:     cm.amount(path+".balance", pot.Balance, base, false),
		Target:  cm.amount(path+".target", pot.Target, base, false),
		Monthly: cm.amount(path+".monthly", pot.Monthly, base, false),
	})
}

func (cm *compiler) linkItemPots() {
	items := cm.plan.Items
	for i := range items {
		ref := items[i].Pot
		if ref == "" {
			continue
		}
		idx, ok := cm.potIdxs[ref]
		if !ok {
			docIdx := indexWhere(cm.budget.Items, func(item Item) bool { return item.ID == items[i].ID })
			path := fmt.Sprintf("items[%d].pot", docIdx)
			cm.add(path, "issue.unknown_pot", args{"id": i18n.Text(ref)})
			continue
		}
		items[i].PotIdx = idx
	}
}

func (cm *compiler) linkDebtPots() {
	debts := cm.plan.Debts
	for i := range debts {
		ref := debts[i].PayoffFrom
		if ref == "" {
			continue
		}
		idx, ok := cm.potIdxs[ref]
		if !ok {
			docIdx := indexWhere(cm.budget.Debts, func(debt Debt) bool { return debt.ID == debts[i].ID })
			path := fmt.Sprintf("debts[%d].payoff_from", docIdx)
			cm.add(path, "issue.unknown_pot", args{"id": i18n.Text(ref)})
			continue
		}
		debts[i].PotIdx = idx
	}
}

func (cm *compiler) compileWithdrawals() {
	seen := map[string]bool{}
	for i, w := range cm.budget.Withdrawals {
		path := fmt.Sprintf("withdrawals[%d]", i)
		cm.id(path+".id", w.ID, seen)
		cm.compileWithdrawal(path, w)
	}
}

func (cm *compiler) compileWithdrawal(path string, w Withdrawal) {
	potIdx, ok := cm.potIdxs[w.Pot]
	if !ok {
		cm.add(path+".pot", "issue.unknown_pot", args{"id": i18n.Text(w.Pot)})
	}
	cm.checkSchedule(path+".schedule", w.Schedule)
	amt := cm.amount(path+".amount", w.Amount, cm.plan.Base, false)
	if !amt.IsPositive() {
		cm.add(path+".amount", "issue.positive", nil)
	}
	cm.plan.Withdrawals = append(cm.plan.Withdrawals, PlanWithdrawal{
		Withdrawal: w,
		Amt:        amt,
		PotIdx:     potIdx,
	})
}

func (cm *compiler) checkScenarios() {
	seen := map[string]bool{}
	for i, s := range cm.budget.Scenarios {
		cm.id(fmt.Sprintf("scenarios[%d].id", i), s.ID, seen)
	}
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
