package budget

import (
	"sort"

	"bbbudget/internal/money"
)

// Rounding policy: the projection is deliberately pessimistic by at most one
// minor unit per conversion. Anything you pay is rounded up (Ceil), anything
// you receive is rounded down (Floor). Interest charged to you rounds up.
// Totals are exact sums of these rounded amounts, so every identity below
// holds to the cent:
//
//	Left    = Income − Expenses − DebtMinCost
//	Saved   = Left − ExtraCost
//	Savings = previous Savings + Saved
const (
	roundPay     = money.Ceil
	roundReceive = money.Floor
)

// Month is one projected month. Base-currency amounts unless noted.
type Month struct {
	Index int    `json:"index"`
	Key   string `json:"month"`
	Label string `json:"label"`

	Income money.Money `json:"income"`
	// ByCategory holds expense totals per expense category id (debt payments excluded).
	ByCategory  map[string]money.Money `json:"by_category"`
	Expenses    money.Money            `json:"expenses"`
	DebtMinCost money.Money            `json:"debt_min_cost"`
	Left        money.Money            `json:"left"`
	ExtraCost   money.Money            `json:"extra_to_debt"`
	// DebtPayments is DebtMinCost + ExtraCost.
	DebtPayments money.Money `json:"debt_payments"`
	Saved        money.Money `json:"saved"`
	Savings      money.Money `json:"savings"`
	Debts        []DebtMonth `json:"debts,omitempty"`
	// DebtBase is all closing debt balances at the mid rate, for charts.
	DebtBase money.Money `json:"debt_balance_base"`
}

// DebtMonth tracks one debt in one month, in the debt's currency unless noted.
type DebtMonth struct {
	ID       string      `json:"id"`
	Interest money.Money `json:"interest"`
	MinPaid  money.Money `json:"min_paid"`
	Extra    money.Money `json:"extra_paid"`
	// Cost is what this month's payments to the debt cost in base currency.
	Cost    money.Money `json:"cost"`
	Balance money.Money `json:"balance"`
}

// Projection is the full month-by-month result.
type Projection struct {
	Plan   *Plan
	Months []Month
}

// Project runs the plan. Arithmetic overflow (only possible with absurd
// inputs) is returned as an error, never a crash.
func Project(p *Plan) (proj *Projection, err error) {
	defer money.Recover(&err)

	base := p.Base
	balances := make([]money.Money, len(p.Debts))
	for i, d := range p.Debts {
		balances[i] = d.Bal
	}
	savings := p.SavingsStart
	out := &Projection{Plan: p, Months: make([]Month, 0, p.Months)}

	for m := 0; m < p.Months; m++ {
		mo := Month{
			Index: m, Key: p.MonthKey(m), Label: p.MonthLabel(m),
			Income: money.Zero(base), Expenses: money.Zero(base), DebtMinCost: money.Zero(base),
			ExtraCost: money.Zero(base), ByCategory: map[string]money.Money{},
		}
		for id, c := range p.Categories {
			if c.Kind == KindExpense {
				mo.ByCategory[id] = money.Zero(base)
			}
		}

		// 1. Interest accrues on the opening balance.
		dm := make([]DebtMonth, len(p.Debts))
		for i, d := range p.Debts {
			cur := d.Bal.Currency()
			dm[i] = DebtMonth{ID: d.ID, Interest: money.Zero(cur), MinPaid: money.Zero(cur), Extra: money.Zero(cur), Cost: money.Zero(base)}
			if balances[i].IsPositive() {
				dm[i].Interest = balances[i].Mul(d.MonthlyRate, roundPay)
				balances[i] = balances[i].Add(dm[i].Interest)
			}
		}

		// 2. Income and expense lines.
		for _, l := range p.Lines {
			if !l.Schedule.ActiveIn(m) {
				continue
			}
			if l.Income {
				mo.Income = mo.Income.Add(p.IncomeValue(l.Amt, roundReceive))
				continue
			}
			cost := p.PayCost(l.Amt, roundPay)
			mo.ByCategory[l.Category] = mo.ByCategory[l.Category].Add(cost)
			mo.Expenses = mo.Expenses.Add(cost)
		}

		// 3. Minimum payments, capped at what is owed.
		for i, d := range p.Debts {
			if !balances[i].IsPositive() {
				continue
			}
			pay := money.Min(d.Min, balances[i])
			balances[i] = balances[i].Sub(pay)
			cost := p.PayCost(pay, roundPay)
			dm[i].MinPaid, dm[i].Cost = pay, cost
			mo.DebtMinCost = mo.DebtMinCost.Add(cost)
		}

		mo.Left = mo.Income.Sub(mo.Expenses).Sub(mo.DebtMinCost)

		// 4. A share of a positive leftover goes to the debts, by strategy.
		if mo.Left.IsPositive() && p.ExtraPct.Sign() > 0 {
			budget := mo.Left.Percent(p.ExtraPct, money.Floor)
			for _, i := range p.payoffOrder(balances) {
				if !budget.IsPositive() {
					break
				}
				var pay, cost money.Money
				if clear := p.PayCost(balances[i], roundPay); clear.Cmp(budget) <= 0 {
					pay, cost = balances[i], clear
				} else {
					// Pay as much as the budget buys; its true cost is <= budget.
					pay = p.Receivable(budget, balances[i].Currency().Code, roundReceive)
					cost = p.PayCost(pay, roundPay)
				}
				if !pay.IsPositive() {
					continue
				}
				balances[i] = balances[i].Sub(pay)
				dm[i].Extra = dm[i].Extra.Add(pay)
				dm[i].Cost = dm[i].Cost.Add(cost)
				budget = budget.Sub(cost)
				mo.ExtraCost = mo.ExtraCost.Add(cost)
			}
		}

		mo.DebtBase = money.Zero(base)
		for i := range dm {
			dm[i].Balance = balances[i]
			mo.DebtBase = mo.DebtBase.Add(p.ToBase(balances[i], money.HalfEven))
		}
		mo.Debts = dm
		mo.DebtPayments = mo.DebtMinCost.Add(mo.ExtraCost)
		mo.Saved = mo.Left.Sub(mo.ExtraCost)
		savings = savings.Add(mo.Saved)
		mo.Savings = savings
		out.Months = append(out.Months, mo)
	}
	return out, nil
}

// payoffOrder lists indexes of debts with a positive balance in the order
// extra payments should go.
func (p *Plan) payoffOrder(balances []money.Money) []int {
	var idx []int
	for i, b := range balances {
		if b.IsPositive() {
			idx = append(idx, i)
		}
	}
	switch p.Strategy {
	case StrategyAvalanche:
		sort.SliceStable(idx, func(a, b int) bool {
			return p.Debts[idx[a]].MonthlyRate.Cmp(p.Debts[idx[b]].MonthlyRate) > 0
		})
	case StrategySnowball:
		// Compare in base currency so mixed-currency debts order correctly.
		sort.SliceStable(idx, func(a, b int) bool {
			return p.ToBase(balances[idx[a]], money.HalfEven).Cmp(p.ToBase(balances[idx[b]], money.HalfEven)) < 0
		})
	}
	return idx
}
