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
//	Left      = Income − IncomeToPots − (Expenses − PaidFromPots) − DebtMinCost − (Payoff − PayoffFromPots)
//	            (the month's own money: items and payoffs routed through pots excluded)
//	Left + Transfers + (Overdraft − previous Overdraft) = ExtraCost + (ToSavings − IncomeToPots)
//	FromSavings = Transfers + PaidFromPots + PayoffFromPots
//	DebtPayments = DebtMinCost + Payoff + ExtraCost
//	Saved     = ToSavings − FromSavings                (net change in savings)
//	Savings   = previous Savings + Saved = sum of the pots
//
// Savings are balances, not income: a short month is never paid for from a
// pot unless a Withdrawal plans it. Otherwise the gap is owed on the current
// account (Overdraft) and repaid from the next surplus before anything is saved.
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
	// IncomeToPots is income routed straight into pots (not spendable).
	IncomeToPots money.Money `json:"income_to_pots"`
	// PaidFromPots is the part of pot-routed costs their pots paid.
	PaidFromPots money.Money `json:"paid_from_pots"`
	// Left is the month's own money: income minus spending, with items routed
	// through pots and savings left out.
	Left money.Money `json:"left"`
	// Payoff is what paying off debts in full cost this month (Debt.PayoffMonth).
	Payoff money.Money `json:"payoff"`
	// PayoffFromPots is the part of Payoff that pots paid.
	PayoffFromPots money.Money `json:"payoff_from_pots"`
	// Transfers is what planned transfers (withdrawals) moved from pots into the month.
	Transfers money.Money `json:"transfers"`
	// FromSavings is everything that left the pots: Transfers + PaidFromPots + PayoffFromPots.
	FromSavings money.Money `json:"from_savings"`
	// Short is how far the month falls short after planned withdrawals.
	// It is owed (Overdraft), never taken from savings silently.
	Short money.Money `json:"short"`
	// OverdraftRepaid is how much of earlier months' overdraft this month repaid.
	OverdraftRepaid money.Money `json:"overdraft_repaid"`
	// Overdraft is owed on the current account at the end of the month.
	Overdraft money.Money `json:"overdraft"`
	ExtraCost money.Money `json:"extra_to_debt"`
	// DebtPayments is DebtMinCost + Payoff + ExtraCost.
	DebtPayments money.Money `json:"debt_payments"`
	// ToSavings is what went into pots.
	ToSavings money.Money `json:"to_savings"`
	// Saved is ToSavings − FromSavings: the net change in savings.
	Saved money.Money `json:"saved"`
	// Savings is all pots together at the end of the month.
	Savings     money.Money       `json:"savings"`
	Pots        []PotMonth        `json:"pots"`
	Withdrawals []WithdrawalMonth `json:"withdrawals,omitempty"`
	// PotCosts are the pot-routed costs due this month; Paid < Cost when the pot ran short.
	PotCosts []PotCostMonth `json:"pot_costs,omitempty"`
	Debts    []DebtMonth    `json:"debts,omitempty"`
	// DebtBase is all closing debt balances at the mid rate, for charts.
	DebtBase money.Money `json:"debt_balance_base"`
}

// DebtMonth tracks one debt in one month, in the debt's currency unless noted.
type DebtMonth struct {
	ID       string      `json:"id"`
	Interest money.Money `json:"interest"`
	MinPaid  money.Money `json:"min_paid"`
	Extra    money.Money `json:"extra_paid"`
	// Payoff is the lump sum paid to clear the debt (Debt.PayoffMonth).
	Payoff money.Money `json:"payoff_paid"`
	// Cost is what this month's payments to the debt cost in base currency.
	Cost    money.Money `json:"cost"`
	Balance money.Money `json:"balance"`
}

// PotMonth tracks one pot in one month.
type PotMonth struct {
	ID      string      `json:"id"`
	In      money.Money `json:"in"`
	Out     money.Money `json:"out"`
	Balance money.Money `json:"balance"`
}

// WithdrawalMonth is a planned withdrawal in a month it applies. Taken is
// less than Planned when the pot didn't hold enough.
type WithdrawalMonth struct {
	ID      string      `json:"id"`
	Planned money.Money `json:"planned"`
	Taken   money.Money `json:"taken"`
}

// PotCostMonth is a cost routed through a pot, in a month it is due.
type PotCostMonth struct {
	ItemID string      `json:"item"`
	Cost   money.Money `json:"cost"`
	Paid   money.Money `json:"paid"`
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
	zero := money.Zero(base)
	pots := make([]money.Money, len(p.Pots))
	for k, pt := range p.Pots {
		pots[k] = pt.Bal
	}
	owed := p.OverdraftStart
	out := &Projection{Plan: p, Months: make([]Month, 0, p.Months)}

	for m := 0; m < p.Months; m++ {
		mo := Month{
			Index: m, Key: p.MonthKey(m), Label: p.MonthLabel(m),
			Income: zero, Expenses: zero, DebtMinCost: zero, ExtraCost: zero, ByCategory: map[string]money.Money{},
			FromSavings: zero, Short: zero, OverdraftRepaid: zero, ToSavings: zero,
			IncomeToPots: zero, PaidFromPots: zero, Transfers: zero, Payoff: zero, PayoffFromPots: zero,
		}
		pm := make([]PotMonth, len(p.Pots))
		for k, pt := range p.Pots {
			pm[k] = PotMonth{ID: pt.ID, In: zero, Out: zero}
		}
		deposit := func(k int, amt money.Money) {
			pots[k] = pots[k].Add(amt)
			pm[k].In = pm[k].In.Add(amt)
			mo.ToSavings = mo.ToSavings.Add(amt)
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
			dm[i] = DebtMonth{ID: d.ID, Interest: money.Zero(cur), MinPaid: money.Zero(cur), Extra: money.Zero(cur), Payoff: money.Zero(cur), Cost: money.Zero(base)}
			if balances[i].IsPositive() {
				dm[i].Interest = balances[i].Mul(d.MonthlyRate, roundPay)
				balances[i] = balances[i].Add(dm[i].Interest)
			}
		}

		// 2. Income and expense items. Items routed through a pot are settled
		// against it below.
		var routed []PlanItem
		var routedAmt []money.Money
		for _, l := range p.Items {
			if !l.Schedule.ActiveIn(m) {
				continue
			}
			amt := p.PayCost(l.Amt, roundPay)
			if l.Income {
				amt = p.IncomeValue(l.Amt, roundReceive)
				mo.Income = mo.Income.Add(amt)
			} else {
				mo.ByCategory[l.Category] = mo.ByCategory[l.Category].Add(amt)
				mo.Expenses = mo.Expenses.Add(amt)
			}
			if l.PotIdx >= 0 {
				routed, routedAmt = append(routed, l), append(routedAmt, amt)
			}
		}

		// 3. Minimum payments, capped at what is owed. A debt paid off this month
		// pays its minimum only on what the payoff leaves (below).
		minPay := func(i int) {
			if !balances[i].IsPositive() {
				return
			}
			pay := money.Min(p.Debts[i].Min, balances[i])
			balances[i] = balances[i].Sub(pay)
			cost := p.PayCost(pay, roundPay)
			dm[i].MinPaid, dm[i].Cost = pay, dm[i].Cost.Add(cost)
			mo.DebtMinCost = mo.DebtMinCost.Add(cost)
		}
		for i, d := range p.Debts {
			if !d.PaysOffIn(m) {
				minPay(i)
			}
		}

		// Routed items: income goes straight into its pot first, then costs are
		// paid from theirs, as far as each pot holds.
		for i, l := range routed {
			if l.Income {
				deposit(l.PotIdx, routedAmt[i])
				mo.IncomeToPots = mo.IncomeToPots.Add(routedAmt[i])
			}
		}
		for i, l := range routed {
			if l.Income {
				continue
			}
			paid := money.Min(routedAmt[i], pots[l.PotIdx])
			pots[l.PotIdx] = pots[l.PotIdx].Sub(paid)
			pm[l.PotIdx].Out = pm[l.PotIdx].Out.Add(paid)
			mo.PaidFromPots = mo.PaidFromPots.Add(paid)
			mo.PotCosts = append(mo.PotCosts, PotCostMonth{ItemID: l.ID, Cost: routedAmt[i], Paid: paid})
		}

		// Payoffs: the whole balance, this month's interest included, from the
		// debt's pot (routed income has landed by now) or the month's own money.
		// A pot pays only what it holds; the rest stays owed and pays its minimum.
		for i, d := range p.Debts {
			if !d.PaysOffIn(m) {
				continue
			}
			if balances[i].IsPositive() {
				pay, cost := balances[i], p.PayCost(balances[i], roundPay)
				if k := d.PotIdx; k >= 0 {
					if cost.Cmp(pots[k]) > 0 {
						// Pay as much as the pot buys; its true cost is <= the pot.
						pay = p.Receivable(pots[k], balances[i].Currency().Code, roundReceive)
						cost = p.PayCost(pay, roundPay)
					}
					pots[k] = pots[k].Sub(cost)
					pm[k].Out = pm[k].Out.Add(cost)
					mo.PayoffFromPots = mo.PayoffFromPots.Add(cost)
				}
				balances[i] = balances[i].Sub(pay)
				dm[i].Payoff, dm[i].Cost = pay, dm[i].Cost.Add(cost)
				mo.Payoff = mo.Payoff.Add(cost)
			}
			minPay(i)
		}

		mo.Left = mo.Income.Sub(mo.IncomeToPots).Sub(mo.Expenses.Sub(mo.PaidFromPots)).Sub(mo.DebtMinCost).Sub(mo.Payoff.Sub(mo.PayoffFromPots))

		// 4. Planned withdrawals, never more than the pot holds.
		for _, w := range p.Withdrawals {
			if !w.Schedule.ActiveIn(m) {
				continue
			}
			take := money.Min(w.Amt, pots[w.PotIdx])
			pots[w.PotIdx] = pots[w.PotIdx].Sub(take)
			pm[w.PotIdx].Out = pm[w.PotIdx].Out.Add(take)
			mo.Transfers = mo.Transfers.Add(take)
			mo.Withdrawals = append(mo.Withdrawals, WithdrawalMonth{ID: w.ID, Planned: w.Amt, Taken: take})
		}

		// 5. A short month is owed, not taken from savings; a surplus first
		// repays what earlier months owe.
		mo.FromSavings = mo.Transfers.Add(mo.PaidFromPots).Add(mo.PayoffFromPots)
		flow := mo.Left.Add(mo.Transfers)
		if flow.IsNegative() {
			mo.Short = flow.Neg()
		}
		rest := zero
		prev := owed
		if flow.Cmp(owed) >= 0 {
			rest, owed = flow.Sub(owed), zero
		} else {
			owed = owed.Sub(flow)
		}
		if owed.Cmp(prev) < 0 {
			mo.OverdraftRepaid = prev.Sub(owed)
		}
		mo.Overdraft = owed

		// 6. Fixed monthly contributions, up to each pot's target.
		for k, pt := range p.Pots {
			if !rest.IsPositive() {
				break
			}
			c := money.Min(pt.Monthly, rest)
			if pt.Target.IsPositive() {
				c = money.Min(c, money.Max(pt.Target.Sub(pots[k]), zero))
			}
			if c.IsPositive() {
				deposit(k, c)
				rest = rest.Sub(c)
			}
		}

		// 7. A share of what is left goes to the debts, by strategy.
		if rest.IsPositive() && p.ExtraPct.Sign() > 0 {
			budget := rest.Percent(p.ExtraPct, money.Floor)
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

		rest = rest.Sub(mo.ExtraCost)

		// 8. Fill pots with a target, in list order; the rest to the default pot.
		for k, pt := range p.Pots {
			if !rest.IsPositive() {
				break
			}
			if room := pt.Target.Sub(pots[k]); pt.Target.IsPositive() && room.IsPositive() {
				c := money.Min(room, rest)
				deposit(k, c)
				rest = rest.Sub(c)
			}
		}
		if rest.IsPositive() {
			deposit(p.DefaultPot, rest)
		}

		mo.DebtBase = money.Zero(base)
		for i := range dm {
			dm[i].Balance = balances[i]
			mo.DebtBase = mo.DebtBase.Add(p.ToBase(balances[i], money.HalfEven))
		}
		mo.Debts = dm
		mo.DebtPayments = mo.DebtMinCost.Add(mo.Payoff).Add(mo.ExtraCost)
		mo.Saved = mo.ToSavings.Sub(mo.FromSavings)
		mo.Savings = zero
		for k := range pm {
			pm[k].Balance = pots[k]
			mo.Savings = mo.Savings.Add(pots[k])
		}
		mo.Pots = pm
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
