package budget

import (
	"slices"

	"bbbudget/internal/money"
)

// Rounding policy: the projection is deliberately pessimistic by at most one
// minor unit per conversion. Anything you pay is rounded up (Ceil), anything
// you receive is rounded down (Floor). Interest charged to you rounds up.
// Totals are exact sums of these rounded amounts, so every identity below
// holds to the cent:
//
//	Left      = Income − IncomeToPots − (Expenses − PaidFromPots) − DebtMinCost
//	            − (Payoff − PayoffFromPots)
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
	// LeftoverToPots is ToSavings − IncomeToPots: the part of savings that came from the leftover.
	LeftoverToPots money.Money `json:"leftover_to_pots"`
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
func Project(p *Plan) (projection *Projection, err error) {
	defer money.Recover(&err)

	state := newMonthState(p)
	result := &Projection{Plan: p, Months: make([]Month, 0, p.Months)}

	for index := 0; index < p.Months; index++ {
		state.startMonth(index)
		state.accrueInterest()
		state.settleItems()
		state.payMinimums()
		state.settleRoutedItems()
		state.payOffDebts()
		state.calculateLeft()
		state.takeWithdrawals()
		state.settleShortfall()
		state.contributeFixed()
		state.payExtraToDebts()
		state.fillPots()
		result.Months = append(result.Months, state.closeMonth())
	}
	return result, nil
}

type monthState struct {
	plan        *Plan
	zero        money.Money
	balances    []money.Money
	pots        []money.Money
	owed        money.Money
	month       Month
	debts       []DebtMonth
	potMonths   []PotMonth
	routed      []routedItem
	unallocated money.Money
}

type routedItem struct {
	item   PlanItem
	amount money.Money
}

func newMonthState(p *Plan) *monthState {
	balances := make([]money.Money, len(p.Debts))
	for i, d := range p.Debts {
		balances[i] = d.Bal
	}
	pots := make([]money.Money, len(p.Pots))
	for k, pt := range p.Pots {
		pots[k] = pt.Bal
	}
	return &monthState{
		plan:     p,
		zero:     money.Zero(p.Base),
		balances: balances,
		pots:     pots,
		owed:     p.OverdraftStart,
	}
}

func (s *monthState) startMonth(index int) {
	s.month = newMonth(s.plan, index)
	s.potMonths = make([]PotMonth, len(s.plan.Pots))
	for k, pt := range s.plan.Pots {
		s.potMonths[k] = PotMonth{ID: pt.ID, In: s.zero, Out: s.zero}
	}
	s.routed = nil
	s.unallocated = s.zero
}

func newMonth(p *Plan, index int) Month {
	zero := money.Zero(p.Base)
	byCategory := map[string]money.Money{}
	for id, c := range p.Categories {
		if c.Kind == KindExpense {
			byCategory[id] = zero
		}
	}
	return Month{
		Index:           index,
		Key:             p.MonthKey(index),
		Label:           p.MonthLabel(index),
		Income:          zero,
		ByCategory:      byCategory,
		Expenses:        zero,
		DebtMinCost:     zero,
		IncomeToPots:    zero,
		PaidFromPots:    zero,
		Payoff:          zero,
		PayoffFromPots:  zero,
		Transfers:       zero,
		FromSavings:     zero,
		Short:           zero,
		OverdraftRepaid: zero,
		ExtraCost:       zero,
		ToSavings:       zero,
	}
}

func (s *monthState) deposit(pot int, amount money.Money) {
	s.pots[pot] = s.pots[pot].Add(amount)
	s.potMonths[pot].In = s.potMonths[pot].In.Add(amount)
	s.month.ToSavings = s.month.ToSavings.Add(amount)
}

func (s *monthState) withdraw(pot int, amount money.Money) {
	s.pots[pot] = s.pots[pot].Sub(amount)
	s.potMonths[pot].Out = s.potMonths[pot].Out.Add(amount)
}

func (s *monthState) accrueInterest() {
	base := s.plan.Base
	s.debts = make([]DebtMonth, len(s.plan.Debts))
	for i, d := range s.plan.Debts {
		currency := d.Bal.Currency()
		s.debts[i] = DebtMonth{
			ID:       d.ID,
			Interest: money.Zero(currency),
			MinPaid:  money.Zero(currency),
			Extra:    money.Zero(currency),
			Payoff:   money.Zero(currency),
			Cost:     money.Zero(base),
		}
		if s.balances[i].IsPositive() {
			interest := s.balances[i].Mul(d.MonthlyRate, roundPay)
			s.debts[i].Interest = interest
			s.balances[i] = s.balances[i].Add(interest)
		}
	}
}

// Items routed through a pot are settled against it later.
func (s *monthState) settleItems() {
	p := s.plan
	for _, item := range p.Items {
		if !item.Schedule.ActiveIn(s.month.Index) {
			continue
		}
		amount := p.PayCost(item.Amt, roundPay)
		if item.Income {
			amount = p.IncomeValue(item.Amt, roundReceive)
			s.month.Income = s.month.Income.Add(amount)
		} else {
			spent := s.month.ByCategory[item.Category].Add(amount)
			s.month.ByCategory[item.Category] = spent
			s.month.Expenses = s.month.Expenses.Add(amount)
		}
		if item.PotIdx >= 0 {
			s.routed = append(s.routed, routedItem{item: item, amount: amount})
		}
	}
}

// payMinimums pays the minimum on every debt not paid off this month. A debt
// paid off this month pays its minimum only on what the payoff leaves.
func (s *monthState) payMinimums() {
	for i, d := range s.plan.Debts {
		if !d.PaysOffIn(s.month.Index) {
			s.payMinimum(i)
		}
	}
}

// payMinimum pays the debt's minimum, capped at what is owed.
func (s *monthState) payMinimum(debt int) {
	if !s.balances[debt].IsPositive() {
		return
	}
	paid := money.Min(s.plan.Debts[debt].Min, s.balances[debt])
	cost := s.plan.PayCost(paid, roundPay)
	s.balances[debt] = s.balances[debt].Sub(paid)
	s.debts[debt].MinPaid = paid
	s.debts[debt].Cost = s.debts[debt].Cost.Add(cost)
	s.month.DebtMinCost = s.month.DebtMinCost.Add(cost)
}

// settleRoutedItems puts routed income into its pot first, then pays routed
// costs from theirs, as far as each pot holds.
func (s *monthState) settleRoutedItems() {
	for _, r := range s.routed {
		if r.item.Income {
			s.deposit(r.item.PotIdx, r.amount)
			s.month.IncomeToPots = s.month.IncomeToPots.Add(r.amount)
		}
	}
	for _, r := range s.routed {
		if r.item.Income {
			continue
		}
		paid := money.Min(r.amount, s.pots[r.item.PotIdx])
		s.withdraw(r.item.PotIdx, paid)
		s.month.PaidFromPots = s.month.PaidFromPots.Add(paid)
		s.month.PotCosts = append(
			s.month.PotCosts,
			PotCostMonth{ItemID: r.item.ID, Cost: r.amount, Paid: paid},
		)
	}
}

// payOffDebts pays the whole balance, this month's interest included, from the
// debt's pot (routed income has landed by now) or the month's own money. A pot
// pays only what it holds; the rest stays owed and pays its minimum.
func (s *monthState) payOffDebts() {
	for i, d := range s.plan.Debts {
		if !d.PaysOffIn(s.month.Index) {
			continue
		}
		if s.balances[i].IsPositive() {
			s.payOff(i)
		}
		s.payMinimum(i)
	}
}

func (s *monthState) payOff(debt int) {
	p := s.plan
	paid := s.balances[debt]
	cost := p.PayCost(paid, roundPay)
	if pot := p.Debts[debt].PotIdx; pot >= 0 {
		if cost.Cmp(s.pots[pot]) > 0 {
			// Pay as much as the pot buys; its true cost is <= the pot.
			paid = p.Receivable(s.pots[pot], paid.Currency().Code, roundReceive)
			cost = p.PayCost(paid, roundPay)
		}
		s.withdraw(pot, cost)
		s.month.PayoffFromPots = s.month.PayoffFromPots.Add(cost)
	}
	s.balances[debt] = s.balances[debt].Sub(paid)
	s.debts[debt].Payoff = paid
	s.debts[debt].Cost = s.debts[debt].Cost.Add(cost)
	s.month.Payoff = s.month.Payoff.Add(cost)
}

func (s *monthState) calculateLeft() {
	m := &s.month
	ownExpenses := m.Expenses.Sub(m.PaidFromPots)
	ownPayoff := m.Payoff.Sub(m.PayoffFromPots)
	m.Left = m.Income.
		Sub(m.IncomeToPots).
		Sub(ownExpenses).
		Sub(m.DebtMinCost).
		Sub(ownPayoff)
}

// takeWithdrawals moves planned withdrawals out of pots, never more than the
// pot holds.
func (s *monthState) takeWithdrawals() {
	for _, w := range s.plan.Withdrawals {
		if !w.Schedule.ActiveIn(s.month.Index) {
			continue
		}
		taken := money.Min(w.Amt, s.pots[w.PotIdx])
		s.withdraw(w.PotIdx, taken)
		s.month.Transfers = s.month.Transfers.Add(taken)
		s.month.Withdrawals = append(
			s.month.Withdrawals,
			WithdrawalMonth{ID: w.ID, Planned: w.Amt, Taken: taken},
		)
	}
}

// settleShortfall owes a short month instead of taking it from savings. A
// surplus first repays what earlier months owe.
func (s *monthState) settleShortfall() {
	m := &s.month
	m.FromSavings = m.Transfers.Add(m.PaidFromPots).Add(m.PayoffFromPots)
	flow := m.Left.Add(m.Transfers)
	if flow.IsNegative() {
		m.Short = flow.Neg()
	}
	previouslyOwed := s.owed
	if flow.Cmp(s.owed) >= 0 {
		s.unallocated = flow.Sub(s.owed)
		s.owed = s.zero
	} else {
		s.owed = s.owed.Sub(flow)
	}
	if s.owed.Cmp(previouslyOwed) < 0 {
		m.OverdraftRepaid = previouslyOwed.Sub(s.owed)
	}
	m.Overdraft = s.owed
}

// A pot with a target takes no more than it has room for.
func (s *monthState) contributeFixed() {
	for k, pt := range s.plan.Pots {
		if !s.unallocated.IsPositive() {
			break
		}
		contribution := money.Min(pt.Monthly, s.unallocated)
		if pt.Target.IsPositive() {
			room := money.Max(pt.Target.Sub(s.pots[k]), s.zero)
			contribution = money.Min(contribution, room)
		}
		if contribution.IsPositive() {
			s.deposit(k, contribution)
			s.unallocated = s.unallocated.Sub(contribution)
		}
	}
}

// payExtraToDebts sends a share of what is left to the debts, by strategy.
func (s *monthState) payExtraToDebts() {
	p := s.plan
	if s.unallocated.IsPositive() && p.ExtraPct.Sign() > 0 {
		extraBudget := s.unallocated.Percent(p.ExtraPct, money.Floor)
		for _, i := range p.payoffOrder(s.balances) {
			if !extraBudget.IsPositive() {
				break
			}
			extraBudget = extraBudget.Sub(s.payExtra(i, extraBudget))
		}
	}
	s.unallocated = s.unallocated.Sub(s.month.ExtraCost)
}

func (s *monthState) payExtra(debt int, extraBudget money.Money) money.Money {
	p := s.plan
	var paid, cost money.Money
	clearCost := p.PayCost(s.balances[debt], roundPay)
	if clearCost.Cmp(extraBudget) <= 0 {
		paid = s.balances[debt]
		cost = clearCost
	} else {
		// Pay as much as the budget buys; its true cost is <= budget.
		paid = p.Receivable(extraBudget, s.balances[debt].Currency().Code, roundReceive)
		cost = p.PayCost(paid, roundPay)
	}
	if !paid.IsPositive() {
		return s.zero
	}
	s.balances[debt] = s.balances[debt].Sub(paid)
	s.debts[debt].Extra = s.debts[debt].Extra.Add(paid)
	s.debts[debt].Cost = s.debts[debt].Cost.Add(cost)
	s.month.ExtraCost = s.month.ExtraCost.Add(cost)
	return cost
}

// Pots with a target fill in list order; the rest goes to the default pot.
func (s *monthState) fillPots() {
	for k, pt := range s.plan.Pots {
		if !s.unallocated.IsPositive() {
			break
		}
		room := pt.Target.Sub(s.pots[k])
		if pt.Target.IsPositive() && room.IsPositive() {
			contribution := money.Min(room, s.unallocated)
			s.deposit(k, contribution)
			s.unallocated = s.unallocated.Sub(contribution)
		}
	}
	if s.unallocated.IsPositive() {
		s.deposit(s.plan.DefaultPot, s.unallocated)
	}
}

func (s *monthState) closeMonth() Month {
	m := s.month
	m.DebtBase = s.zero
	for i := range s.debts {
		s.debts[i].Balance = s.balances[i]
		m.DebtBase = m.DebtBase.Add(s.plan.ToBase(s.balances[i], money.HalfEven))
	}
	m.Debts = s.debts
	m.DebtPayments = m.DebtMinCost.Add(m.Payoff).Add(m.ExtraCost)
	m.Saved = m.ToSavings.Sub(m.FromSavings)
	m.LeftoverToPots = m.ToSavings.Sub(m.IncomeToPots)
	m.Savings = s.zero
	for k := range s.potMonths {
		s.potMonths[k].Balance = s.pots[k]
		m.Savings = m.Savings.Add(s.pots[k])
	}
	m.Pots = s.potMonths
	return m
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
		slices.SortStableFunc(idx, func(a, b int) int {
			return p.Debts[b].MonthlyRate.Cmp(p.Debts[a].MonthlyRate)
		})
	case StrategySnowball:
		// Compare in base currency so mixed-currency debts order correctly.
		slices.SortStableFunc(idx, func(a, b int) int {
			balanceA := p.ToBase(balances[a], money.HalfEven)
			balanceB := p.ToBase(balances[b], money.HalfEven)
			return balanceA.Cmp(balanceB)
		})
	}
	return idx
}
