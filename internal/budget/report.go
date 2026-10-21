package budget

import (
	"math/big"

	"bbbudget/internal/i18n"
	"bbbudget/internal/money"
)

// ItemReport is an item with its cost in base currency.
type ItemReport struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	Category string      `json:"category"`
	Income   bool        `json:"income"`
	Amount   money.Money `json:"amount"`
	// PerOccurrence is one occurrence in base, transfer loss included for costs.
	PerOccurrence money.Money `json:"per_occurrence_base"`
	// MonthlyEquivalent spreads a recurring item over its period; zero for one-offs.
	MonthlyEquivalent money.Money `json:"monthly_equivalent_base"`
	Schedule          Schedule    `json:"schedule"`
	ScheduleText      string      `json:"schedule_text"`
}

// CategoryTotal is what a category's items come to in base currency.
type CategoryTotal struct {
	// MonthlyEquivalent is the sum of the recurring items' monthly equivalents.
	MonthlyEquivalent money.Money `json:"monthly_equivalent_base"`
	// OneOff is the sum of the one-off items' amounts.
	OneOff money.Money `json:"one_off_base"`
}

// Totals summarises the whole window, base currency.
type Totals struct {
	Income        money.Money `json:"income"`
	Expenses      money.Money `json:"expenses"`
	DebtPayments  money.Money `json:"debt_payments"`
	Saved         money.Money `json:"saved"`
	EndSavings    money.Money `json:"end_savings"`
	TotalInterest money.Money `json:"total_interest_base"`
}

// Report is everything a UI or an assistant needs, computed in one place.
type Report struct {
	Name         string `json:"name"`
	BaseCurrency string `json:"base_currency"`
	// StartMonth is the first projected month, "YYYY-MM".
	StartMonth string        `json:"start_month"`
	Months     []Month       `json:"months"`
	Items      []ItemReport  `json:"items"`
	Debts      []DebtSummary `json:"debts"`
	// Pots are the savings pots; Goals repeats the ones with a target.
	Pots  []PotSummary `json:"pots"`
	Goals []GoalStatus `json:"goals"`
	// Shortfalls are the months that fall short, each with the pots that
	// could cover it. Nothing is taken from savings until a withdrawal says so.
	Shortfalls []Shortfall `json:"shortfalls"`
	Totals     Totals      `json:"totals"`
	Insights   []Insight   `json:"insights"`
	// Currencies lists each foreign currency with what one unit is worth.
	Currencies []CurrencyReport `json:"currencies"`
	// CategoryTotals maps each category id to the totals of its items.
	CategoryTotals map[string]CategoryTotal `json:"category_totals"`
}

// CurrencyReport is a foreign currency as a UI shows it: "1 BTC = €73,957.31",
// "1,000 KRW = €0.61".
type CurrencyReport struct {
	Code   string `json:"code"`
	Crypto bool   `json:"crypto"`
	// Units is the smallest power of ten of the currency worth at least a
	// tenth of a unit of base (1 for BTC or USD, 1,000 for KRW), so BaseValue
	// keeps two significant digits instead of rounding to nothing.
	Units money.Decimal `json:"units"`
	// BaseValue is Units of the currency in base, at the mid rate.
	BaseValue money.Money `json:"base_value"`
}

// Analyze compiles, projects and summarises a budget.
func Analyze(b Budget) (*Report, error) {
	p, err := Compile(b)
	if err != nil {
		return nil, err
	}
	pr, err := Project(p)
	if err != nil {
		return nil, err
	}
	return buildReport(pr)
}

func buildReport(pr *Projection) (r *Report, err error) {
	defer money.Recover(&err)
	p := pr.Plan
	r = &Report{
		Name: p.Doc.Name, BaseCurrency: p.Base.Code, StartMonth: p.Doc.StartMonth, Months: pr.Months,
		Debts: pr.DebtSummaries(), Pots: pr.PotSummaries(), Goals: pr.GoalStatuses(), Shortfalls: pr.Shortfalls(),
		Items:    []ItemReport{},
		Insights: Insights(pr, DefaultRules),
	}
	for _, l := range p.Items {
		per := p.PayCost(l.Amt, roundPay)
		if l.Income {
			per = p.IncomeValue(l.Amt, roundReceive)
		}
		r.Items = append(r.Items, ItemReport{
			ID: l.ID, Name: l.Name, Category: l.Category, Income: l.Income, Amount: l.Amt,
			PerOccurrence: per, MonthlyEquivalent: p.MonthlyEquivalent(l),
			Schedule: l.Schedule,
		})
	}
	r.CategoryTotals = totalItemsByCategory(p, r.Items)
	r.Currencies = []CurrencyReport{}
	for _, c := range p.Doc.Currencies {
		cur, _ := p.Currency(c.Code)
		units, value := p.unitValue(cur)
		r.Currencies = append(r.Currencies, CurrencyReport{c.Code, cur.Crypto, units, value})
	}
	t := Totals{Income: money.Zero(p.Base), Expenses: money.Zero(p.Base), DebtPayments: money.Zero(p.Base), Saved: money.Zero(p.Base)}
	for _, m := range pr.Months {
		t.Income = t.Income.Add(m.Income)
		t.Expenses = t.Expenses.Add(m.Expenses)
		t.DebtPayments = t.DebtPayments.Add(m.DebtMinCost).Add(m.ExtraCost)
		t.Saved = t.Saved.Add(m.Saved)
	}
	t.EndSavings = pr.Last().Savings
	t.TotalInterest = pr.TotalInterestBase()
	r.Totals = t
	r.Localize(i18n.For(i18n.Default))
	return r, nil
}

func totalItemsByCategory(p *Plan, items []ItemReport) map[string]CategoryTotal {
	zero := money.Zero(p.Base)
	totals := map[string]CategoryTotal{}
	for id := range p.Categories {
		totals[id] = CategoryTotal{MonthlyEquivalent: zero, OneOff: zero}
	}
	for _, item := range items {
		total, ok := totals[item.Category]
		if !ok {
			continue
		}
		total.MonthlyEquivalent = total.MonthlyEquivalent.Add(item.MonthlyEquivalent)
		if item.Schedule.Every == 0 {
			total.OneOff = total.OneOff.Add(item.PerOccurrence)
		}
		totals[item.Category] = total
	}
	return totals
}

// unitValue finds the smallest 10^k units of cur worth at least 0.1 in base.
func (p *Plan) unitValue(cur money.Currency) (money.Decimal, money.Money) {
	enough := big.NewRat(1, 10)
	units := int64(1)
	m, _ := money.FromDecimal(money.DecimalFromInt(1), cur) // 1 always fits
	v := p.ToBase(m, money.HalfEven)
	for v.Rat().Cmp(enough) < 0 && units < MaxAmountMajor {
		next, err := money.FromDecimal(money.DecimalFromInt(units*10), cur)
		if err != nil {
			break
		}
		units, v = units*10, p.ToBase(next, money.HalfEven)
	}
	return money.DecimalFromInt(units), v
}
