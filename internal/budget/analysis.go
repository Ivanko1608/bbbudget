package budget

import (
	"math/big"

	"bbbudget/internal/money"
)

// DebtSummary describes one debt over the projection window.
type DebtSummary struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	StartBalance  money.Money `json:"start_balance"`
	EndBalance    money.Money `json:"end_balance"`
	TotalInterest money.Money `json:"total_interest"`
	// PaidOffMonth is the index of the month the balance reaches zero, or nil.
	PaidOffMonth *int   `json:"paid_off_month"`
	PaidOffLabel string `json:"paid_off_label,omitempty"`
}

// GoalStatus says when savings first reach a goal.
type GoalStatus struct {
	ID     string      `json:"id"`
	Name   string      `json:"name"`
	Target money.Money `json:"target"`
	// AlreadyMet is true when savings_start already covers the target.
	AlreadyMet bool `json:"already_met"`
	// ReachedMonth is the first month index with savings >= target, or nil.
	ReachedMonth *int   `json:"reached_month"`
	ReachedLabel string `json:"reached_label,omitempty"`
	// NeededPerMonth is the saving rate that would reach the target by the end
	// of the window, set only when it is not reached.
	NeededPerMonth *money.Money `json:"needed_per_month,omitempty"`
}

func (pr *Projection) Last() Month { return pr.Months[len(pr.Months)-1] }

func (pr *Projection) DebtSummaries() []DebtSummary {
	p := pr.Plan
	out := make([]DebtSummary, len(p.Debts))
	for i, d := range p.Debts {
		s := DebtSummary{ID: d.ID, Name: d.Name, StartBalance: d.Bal, TotalInterest: money.Zero(d.Bal.Currency()), EndBalance: d.Bal}
		for _, mo := range pr.Months {
			dm := mo.Debts[i]
			s.TotalInterest = s.TotalInterest.Add(dm.Interest)
			s.EndBalance = dm.Balance
			if s.PaidOffMonth == nil && d.Bal.IsPositive() && dm.Balance.IsZero() {
				m := mo.Index
				s.PaidOffMonth, s.PaidOffLabel = &m, mo.Label
			}
		}
		out[i] = s
	}
	return out
}

func (pr *Projection) GoalStatuses() []GoalStatus {
	p := pr.Plan
	out := make([]GoalStatus, len(p.Goals))
	for i, g := range p.Goals {
		s := GoalStatus{ID: g.ID, Name: g.Name, Target: g.Amt}
		if p.SavingsStart.Cmp(g.Amt) >= 0 {
			s.AlreadyMet = true
		} else {
			for _, mo := range pr.Months {
				if mo.Savings.Cmp(g.Amt) >= 0 {
					m := mo.Index
					s.ReachedMonth, s.ReachedLabel = &m, mo.Label
					break
				}
			}
			if s.ReachedMonth == nil {
				need := g.Amt.Sub(p.SavingsStart).Mul(big.NewRat(1, int64(p.Months)), money.Ceil)
				s.NeededPerMonth = &need
			}
		}
		out[i] = s
	}
	return out
}

// TotalInterestBase is all interest over the window, converted to base.
func (pr *Projection) TotalInterestBase() money.Money {
	t := money.Zero(pr.Plan.Base)
	for _, s := range pr.DebtSummaries() {
		t = t.Add(pr.Plan.ToBase(s.TotalInterest, money.HalfEven))
	}
	return t
}

// MonthlyEquivalent is a recurring line's cost (or income) per month in base:
// a quarterly €300 line is €100/month. One-off lines return zero.
func (p *Plan) MonthlyEquivalent(l PlanLine) money.Money {
	if l.Schedule.Every == 0 {
		return money.Zero(p.Base)
	}
	per := p.PayCost(l.Amt, roundPay)
	if l.Income {
		per = p.IncomeValue(l.Amt, roundReceive)
	}
	return per.Mul(big.NewRat(1, int64(l.Schedule.Every)), money.HalfEven)
}

// RecurringIncome is the monthly-equivalent income from recurring lines active at month 0.
func (p *Plan) RecurringIncome() money.Money {
	t := money.Zero(p.Base)
	for _, l := range p.Lines {
		if l.Income && l.Schedule.Start == 0 {
			t = t.Add(p.MonthlyEquivalent(l))
		}
	}
	return t
}
