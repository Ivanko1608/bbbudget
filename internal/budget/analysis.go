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

// GoalStatus says when a pot with a target first reaches it.
type GoalStatus struct {
	ID     string      `json:"id"`
	Name   string      `json:"name"`
	Target money.Money `json:"target"`
	// AlreadyMet is true when the pot's starting balance covers the target.
	AlreadyMet bool `json:"already_met"`
	// ReachedMonth is the first month index with the pot at its target, or nil.
	ReachedMonth *int   `json:"reached_month"`
	ReachedLabel string `json:"reached_label,omitempty"`
	// NeededPerMonth is the saving rate into this pot that would reach the
	// target by the end of the window, set only when it is not reached.
	NeededPerMonth *money.Money `json:"needed_per_month,omitempty"`
}

// PotSummary describes one savings pot over the window.
type PotSummary struct {
	ID    string      `json:"id"`
	Name  string      `json:"name"`
	Kind  string      `json:"kind"`
	Start money.Money `json:"start"`
	End   money.Money `json:"end"`
	In    money.Money `json:"in"`
	Out   money.Money `json:"out"`
	// Default is true for the pot that receives unassigned leftover.
	Default bool        `json:"default"`
	Goal    *GoalStatus `json:"goal,omitempty"`
}

// CoverOption is a pot that could pay for a short month.
type CoverOption struct {
	PotID string `json:"pot"`
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	// Available is the pot's balance at the start of that month, after
	// withdrawals already planned for it.
	Available money.Money `json:"available"`
	// Covers is how much of the shortfall this pot alone could pay.
	Covers money.Money `json:"covers"`
}

// Shortfall is a month that falls short, with the pots that could cover it,
// general savings first and the emergency fund last.
type Shortfall struct {
	Month   int           `json:"month"`
	Label   string        `json:"label"`
	Amount  money.Money   `json:"amount"`
	Options []CoverOption `json:"options"`
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

// PotSummaries lists every pot with its flows and, if it has a target, when
// the target is reached.
func (pr *Projection) PotSummaries() []PotSummary {
	p := pr.Plan
	zero := money.Zero(p.Base)
	out := make([]PotSummary, len(p.Pots))
	for k, pt := range p.Pots {
		s := PotSummary{ID: pt.ID, Name: pt.Name, Kind: pt.Kind, Start: pt.Bal, End: pt.Bal, In: zero, Out: zero, Default: k == p.DefaultPot}
		var g *GoalStatus
		if pt.Target.IsPositive() {
			g = &GoalStatus{ID: pt.ID, Name: pt.Name, Target: pt.Target, AlreadyMet: pt.Bal.Cmp(pt.Target) >= 0}
		}
		for _, mo := range pr.Months {
			pm := mo.Pots[k]
			s.In, s.Out, s.End = s.In.Add(pm.In), s.Out.Add(pm.Out), pm.Balance
			if g != nil && !g.AlreadyMet && g.ReachedMonth == nil && pm.Balance.Cmp(pt.Target) >= 0 {
				m := mo.Index
				g.ReachedMonth, g.ReachedLabel = &m, mo.Label
			}
		}
		if g != nil && !g.AlreadyMet && g.ReachedMonth == nil {
			need := pt.Target.Sub(pt.Bal).Mul(big.NewRat(1, int64(p.Months)), money.Ceil)
			g.NeededPerMonth = &need
		}
		s.Goal = g
		out[k] = s
	}
	return out
}

// GoalStatuses are the goals of the pots that have a target, in pot order.
func (pr *Projection) GoalStatuses() []GoalStatus {
	out := []GoalStatus{}
	for _, s := range pr.PotSummaries() {
		if s.Goal != nil {
			out = append(out, *s.Goal)
		}
	}
	return out
}

// Shortfalls lists the months that fall short and what could cover each.
func (pr *Projection) Shortfalls() []Shortfall {
	p := pr.Plan
	out := []Shortfall{}
	for _, mo := range pr.Months {
		if !mo.Short.IsPositive() {
			continue
		}
		sf := Shortfall{Month: mo.Index, Label: mo.Label, Amount: mo.Short, Options: []CoverOption{}}
		for _, kind := range PotKinds {
			for k, pt := range p.Pots {
				if pt.Kind != kind {
					continue
				}
				// A short month adds nothing from its leftover, so the closing
				// balance is what the pot can still give after this month's
				// routed income, costs and transfers.
				avail := mo.Pots[k].Balance
				if avail.IsPositive() {
					sf.Options = append(sf.Options, CoverOption{PotID: pt.ID, Name: pt.Name, Kind: pt.Kind, Available: avail, Covers: money.Min(avail, mo.Short)})
				}
			}
		}
		out = append(out, sf)
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
