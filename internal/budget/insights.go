package budget

import (
	"math/big"
	"sort"
	"strings"

	"bbbudget/internal/i18n"
	"bbbudget/internal/money"
)

// Tone is the severity of an insight.
type Tone string

// Insight tones.
const (
	ToneInfo Tone = "info"
	ToneWarn Tone = "warn"
	ToneGood Tone = "good"
)

// Insight is one plain-language observation. The rule sets the message (a
// catalog key and typed arguments); Text is that message rendered in the
// reader's language (see Report.Localize) and may contain **bold** spans.
type Insight struct {
	ID   string `json:"id"`
	Tone Tone   `json:"tone"`
	i18n.Msg
	Text string `json:"text"`
}

// args is shorthand for message arguments.
type args = map[string]i18n.Arg

func msg(key string, a args) i18n.Msg { return i18n.Msg{Key: key, Args: a} }

// joinWith joins messages with a conjunction message ("list.or" is
// "{a} or {b}"), so each language words its own "or" and "and".
func joinWith(key string, ms []i18n.Msg) i18n.Msg {
	out := ms[len(ms)-1]
	for i := len(ms) - 2; i >= 0; i-- {
		out = msg(key, args{"a": i18n.Nested(ms[i]), "b": i18n.Nested(out)})
	}
	return out
}

// Rule produces zero or more insights from a projection. Rules must be pure.
type Rule struct {
	ID  string
	Run func(pr *Projection) []Insight
}

// DefaultRules is the ordered registry. Add a Rule here to add an insight.
var DefaultRules = []Rule{
	{"category_share", ruleCategoryShare},
	{"transfer_loss", ruleTransferLoss},
	{"fx_risk", ruleFXRisk},
	{"crypto_income", ruleCryptoIncome},
	{"cash_out_loss", ruleCashOutLoss},
	{"one_offs", ruleOneOffs},
	{"first_month", ruleFirstMonth},
	{"biggest_item", ruleBiggestItem},
	{"debt", ruleDebt},
	{"debt_payoff", ruleDebtPayoff},
	{"goals", ruleGoals},
	{"shortfall", ruleShortfall},
	{"withdrawal_cut", ruleWithdrawalCut},
	{"emergency_fund", ruleEmergencyFund},
}

// Insights runs rules in order.
func Insights(pr *Projection, rules []Rule) []Insight {
	var out []Insight
	for _, r := range rules {
		for _, in := range r.Run(pr) {
			if in.ID == "" {
				in.ID = r.ID
			}
			out = append(out, in)
		}
	}
	return out
}

// fxRate is an exchange rate to 4 significant digits, so that a crypto rate
// (0.00001352 BTC per €) doesn't print as 0.00.
func fxRate(r *big.Rat) i18n.Arg { return i18n.Num(money.RoundSignificant(r, 4).String()) }

// pctArg is pct as a message argument.
func pctArg(a, b money.Money) i18n.Arg { return i18n.Int(pct(a, b)) }

// pct returns round(100*a/b) as an int, or 0 when b is not positive.
func pct(a, b money.Money) int64 {
	if !b.IsPositive() {
		return 0
	}
	r := new(big.Rat).Quo(a.Rat(), b.Rat())
	return money.RoundInt(r.Mul(r, big.NewRat(100, 1)), money.HalfEven).Int64()
}

func ruleCategoryShare(pr *Projection) []Insight {
	p := pr.Plan
	inc := p.RecurringIncome()
	totals := map[string]money.Money{}
	for _, l := range p.Items {
		if !l.Income && l.Schedule.Start == 0 {
			t, ok := totals[l.Category]
			if !ok {
				t = money.Zero(p.Base)
			}
			totals[l.Category] = t.Add(p.MonthlyEquivalent(l))
		}
	}
	var parts []i18n.Msg
	for _, c := range p.Doc.Categories {
		if t, ok := totals[c.ID]; ok && t.IsPositive() {
			a := args{"label": i18n.Text(c.Label), "amount": i18n.Money(t), "pct": pctArg(t, inc)}
			parts = append(parts, msg("insight.category_share.item", a))
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return []Insight{{Tone: ToneInfo, Msg: msg("insight.category_share", args{"items": i18n.List(parts)})}}
}

// foreignRecurring sums recurring outgoing amounts per foreign currency, in that currency.
func foreignRecurring(p *Plan) map[string]money.Money {
	out := map[string]money.Money{}
	add := func(m money.Money, every int) {
		code := m.Currency().Code
		if code == p.Base.Code || every == 0 {
			return
		}
		t, ok := out[code]
		if !ok {
			t = money.Zero(m.Currency())
		}
		out[code] = t.Add(m.Mul(big.NewRat(1, int64(every)), money.HalfEven))
	}
	for _, l := range p.Items {
		if !l.Income && l.Schedule.Start == 0 {
			add(l.Amt, l.Schedule.Every)
		}
	}
	for _, d := range p.Debts {
		if d.Bal.IsPositive() {
			add(money.Min(d.Min, d.Bal), 1)
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	k := make([]string, 0, len(m))
	for s := range m {
		k = append(k, s)
	}
	sort.Strings(k)
	return k
}

func ruleTransferLoss(pr *Projection) []Insight {
	p := pr.Plan
	var out []Insight
	fr := foreignRecurring(p)
	for _, code := range sortedKeys(fr) {
		amt := fr[code]
		fee := p.FeePct(code)
		if fee.Sign() == 0 || !amt.IsPositive() {
			continue
		}
		mid := p.ToBase(amt, money.HalfEven)
		loss := p.PayCost(amt, money.HalfEven).Sub(mid)
		perPoint := mid.Mul(big.NewRat(1, 100), money.HalfEven)
		tone := ToneInfo
		if fee.Cmp(big.NewRat(4, 1)) > 0 {
			tone = ToneWarn
		}
		a := args{
			"amount":    i18n.Money(amt),
			"loss":      i18n.Money(loss),
			"loss_year": i18n.Money(loss.Mul(big.NewRat(12, 1), money.HalfEven)),
			"per_point": i18n.Money(perPoint),
		}
		out = append(out, Insight{ID: "transfer_loss_" + strings.ToLower(code), Tone: tone, Msg: msg("insight.transfer_loss", a)})
	}
	return out
}

func ruleFXRisk(pr *Projection) []Insight {
	p := pr.Plan
	var out []Insight
	fr := foreignRecurring(p)
	for _, code := range sortedKeys(fr) {
		amt := fr[code]
		if !amt.IsPositive() {
			continue
		}
		rate := p.rates[code].perBase
		strong := new(big.Rat).Mul(rate, big.NewRat(9, 10)) // currency 10% stronger
		weak := new(big.Rat).Mul(rate, big.NewRat(11, 10))
		now := p.PayCost(amt, money.HalfEven)
		atStrong := p.WithRate(code, strong).PayCost(amt, money.HalfEven)
		atWeak := p.WithRate(code, weak).PayCost(amt, money.HalfEven)
		a := args{
			"code":      i18n.Text(code),
			"base":      i18n.Text(p.Base.Code),
			"strong":    fxRate(strong),
			"at_strong": i18n.Money(atStrong),
			"delta":     i18n.Money(atStrong.Sub(now)),
			"weak":      fxRate(weak),
			"at_weak":   i18n.Money(atWeak),
		}
		out = append(out, Insight{ID: "fx_risk_" + strings.ToLower(code), Tone: ToneInfo, Msg: msg("insight.fx_risk", a)})
	}
	return out
}

// foreignIncome sums recurring income per foreign currency, per month, in that currency.
func foreignIncome(p *Plan) map[string]money.Money {
	out := map[string]money.Money{}
	for _, l := range p.Items {
		c := l.Amt.Currency()
		if !l.Income || c.Code == p.Base.Code || l.Schedule.Every == 0 || l.Schedule.Start != 0 {
			continue
		}
		t, ok := out[c.Code]
		if !ok {
			t = money.Zero(c)
		}
		out[c.Code] = t.Add(l.Amt.Mul(big.NewRat(1, int64(l.Schedule.Every)), money.HalfEven))
	}
	return out
}

// ruleCryptoIncome sizes the risk of income paid in a volatile cryptocurrency
// (stablecoins are covered by the FX rules like any other currency).
func ruleCryptoIncome(pr *Projection) []Insight {
	p := pr.Plan
	fi := foreignIncome(p)
	total := money.Zero(p.Base)
	var codes []string
	for _, code := range sortedKeys(fi) {
		if c := fi[code].Currency(); c.Crypto && !c.Stable && fi[code].IsPositive() {
			total = total.Add(p.IncomeValue(fi[code], roundReceive))
			codes = append(codes, code)
		}
	}
	if !total.IsPositive() {
		return nil
	}
	drop := total.Mul(big.NewRat(3, 10), money.Ceil)
	m := pr.Months[0]
	a := args{
		"pct":   pctArg(total, p.RecurringIncome()),
		"total": i18n.Money(total),
		"codes": i18n.Text(strings.Join(codes, ", ")),
		"drop":  i18n.Money(drop),
		"month": i18n.Month(m.Key),
	}
	after := m.Left.Sub(drop)
	if after.IsNegative() {
		a["short"] = i18n.Money(after.Neg())
		return []Insight{{Tone: ToneWarn, Msg: msg("insight.crypto_income.short", a)}}
	}
	a["left"] = i18n.Money(after)
	return []Insight{{Tone: ToneInfo, Msg: msg("insight.crypto_income.ok", a)}}
}

func ruleCashOutLoss(pr *Projection) []Insight {
	p := pr.Plan
	var out []Insight
	fi := foreignIncome(p)
	for _, code := range sortedKeys(fi) {
		amt := fi[code]
		if p.CashOutPct(code).Sign() == 0 || !amt.IsPositive() {
			continue
		}
		loss := p.ToBase(amt, money.HalfEven).Sub(p.IncomeValue(amt, money.HalfEven))
		a := args{
			"code":      i18n.Text(code),
			"amount":    i18n.Money(amt),
			"loss":      i18n.Money(loss),
			"loss_year": i18n.Money(loss.Mul(big.NewRat(12, 1), money.HalfEven)),
		}
		out = append(out, Insight{ID: "cash_out_loss_" + strings.ToLower(code), Tone: ToneInfo, Msg: msg("insight.cash_out_loss", a)})
	}
	return out
}

func ruleOneOffs(pr *Projection) []Insight {
	p := pr.Plan
	var out []Insight
	for _, l := range p.Items {
		if l.Schedule.Every != 0 || l.Schedule.Start >= p.Months {
			continue
		}
		key := "insight.one_off.cost"
		amt := p.PayCost(l.Amt, roundPay)
		if l.Income {
			key = "insight.one_off.income"
			amt = p.IncomeValue(l.Amt, roundReceive)
		}
		a := args{"name": i18n.Text(l.Name), "amount": i18n.Money(amt), "month": i18n.Month(p.MonthKey(l.Schedule.Start))}
		out = append(out, Insight{ID: "one_off_" + l.ID, Tone: ToneInfo, Msg: msg(key, a)})
	}
	return out
}

func ruleFirstMonth(pr *Projection) []Insight {
	m := pr.Months[0]
	if m.Left.IsNegative() {
		return nil // ruleShortfall says what to do about it
	}
	inc := m.Income
	thin := !inc.IsPositive() || pct(m.Left, inc) < 5
	day := m.Left.Mul(big.NewRat(1, 30), money.Floor)
	a := args{"month": i18n.Month(m.Key), "left": i18n.Money(m.Left), "per_day": i18n.Money(day)}
	if thin {
		return []Insight{{Tone: ToneWarn, Msg: msg("insight.first_month.thin", a)}}
	}
	return []Insight{{Tone: ToneGood, Msg: msg("insight.first_month.left", a)}}
}

func ruleBiggestItem(pr *Projection) []Insight {
	p := pr.Plan
	var best *PlanItem
	var bestAmt money.Money
	for i, l := range p.Items {
		if l.Income || l.Schedule.Every == 0 {
			continue
		}
		if a := p.MonthlyEquivalent(l); best == nil || a.Cmp(bestAmt) > 0 {
			best, bestAmt = &p.Items[i], a
		}
	}
	if best == nil {
		return nil
	}
	a := args{"name": i18n.Text(best.Name), "amount": i18n.Money(bestAmt), "pct": pctArg(bestAmt, p.RecurringIncome())}
	return []Insight{{Tone: ToneInfo, Msg: msg("insight.biggest_item", a)}}
}

func ruleDebt(pr *Projection) []Insight {
	p := pr.Plan
	var out []Insight
	for i, s := range pr.DebtSummaries() {
		d := p.Debts[i]
		if !d.Bal.IsPositive() {
			continue
		}
		interest := d.Bal.Mul(d.MonthlyRate, roundPay)
		minArgs := args{"name": i18n.Text(d.Name), "min": i18n.Money(d.Min), "interest": i18n.Money(interest)}
		if d.Min.Cmp(interest) <= 0 {
			out = append(out, Insight{ID: "debt_min_" + d.ID, Tone: ToneWarn, Msg: msg("insight.debt.min_short", minArgs)})
		} else {
			minArgs["principal"] = i18n.Money(d.Min.Sub(interest))
			out = append(out, Insight{ID: "debt_min_" + d.ID, Tone: ToneInfo, Msg: msg("insight.debt.min_split", minArgs)})
		}
		totalArgs := args{
			"name":          i18n.Text(d.Name),
			"interest":      i18n.Money(s.TotalInterest),
			"interest_base": i18n.Money(p.ToBase(s.TotalInterest, money.HalfEven)),
			"count":         i18n.Int(int64(p.Months)),
		}
		key, tone := "insight.debt.not_paid", ToneWarn
		if s.PaidOffMonth != nil {
			key, tone = "insight.debt.paid_off", ToneGood
			totalArgs["month"] = i18n.Month(p.MonthKey(*s.PaidOffMonth))
		}
		out = append(out, Insight{ID: "debt_total_" + d.ID, Tone: tone, Msg: msg(key, totalArgs)})
	}
	return out
}

// ruleDebtPayoff says what paying a debt off in full costs, where the money
// comes from, and what stays owed when the pot can't cover it.
func ruleDebtPayoff(pr *Projection) []Insight {
	p := pr.Plan
	var out []Insight
	for i, d := range p.Debts {
		if d.PayoffMonth == nil || *d.PayoffMonth >= len(pr.Months) {
			continue
		}
		m := *d.PayoffMonth
		mo, dm := pr.Months[m], pr.Months[m].Debts[i]
		owing := d.Bal
		if m > 0 {
			owing = pr.Months[m-1].Debts[i].Balance
		}
		if owing = owing.Add(dm.Interest); !owing.IsPositive() {
			continue // already paid off by then
		}
		cost := p.PayCost(dm.Payoff, roundPay)
		from := i18n.Nested(msg("insight.debt_payoff.month_money", nil))
		if d.PotIdx >= 0 {
			from = i18n.Text(p.Pots[d.PotIdx].Name)
		}
		amount := i18n.Money(dm.Payoff)
		if dm.Payoff.Currency() != p.Base {
			amount = i18n.Nested(msg("insight.amount_in_base", args{"amount": amount, "base": i18n.Money(cost)}))
		}
		a := args{"name": i18n.Text(d.Name), "from": from, "month": i18n.Month(mo.Key)}
		if rest := owing.Sub(dm.Payoff); rest.IsPositive() {
			a["rest"] = i18n.Money(rest)
			key := "insight.debt_payoff.partial"
			if dm.Payoff.IsPositive() {
				a["held"] = i18n.Money(cost)
				a["paid"] = i18n.Money(dm.Payoff)
			} else {
				key = "insight.debt_payoff.none"
			}
			out = append(out, Insight{ID: "debt_payoff_" + d.ID, Tone: ToneWarn, Msg: msg(key, a)})
			continue
		}
		a["amount"] = amount
		out = append(out, Insight{ID: "debt_payoff_" + d.ID, Tone: ToneGood, Msg: msg("insight.debt_payoff.full", a)})
	}
	return out
}

func ruleGoals(pr *Projection) []Insight {
	var out []Insight
	for _, g := range pr.GoalStatuses() {
		a := args{"name": i18n.Text(g.Name), "target": i18n.Money(g.Target)}
		switch {
		case g.AlreadyMet:
			out = append(out, Insight{ID: "goal_" + g.ID, Tone: ToneGood, Msg: msg("insight.goal.met", a)})
		case g.ReachedMonth != nil:
			a["month"] = i18n.Month(pr.Plan.MonthKey(*g.ReachedMonth))
			out = append(out, Insight{ID: "goal_" + g.ID, Tone: ToneGood, Msg: msg("insight.goal.reached", a)})
		default:
			a["count"] = i18n.Int(int64(pr.Plan.Months))
			a["needed"] = i18n.Money(*g.NeededPerMonth)
			out = append(out, Insight{ID: "goal_" + g.ID, Tone: ToneWarn, Msg: msg("insight.goal.missed", a)})
		}
	}
	return out
}

// ruleShortfall reports short months without touching savings, and says
// which pots could cover the first one.
func ruleShortfall(pr *Projection) []Insight {
	sfs := pr.Shortfalls()
	if len(sfs) == 0 {
		return nil
	}
	sf := sfs[0]
	sentences := []i18n.Msg{msg("insight.shortfall.short", args{"month": i18n.Month(pr.Plan.MonthKey(sf.Month)), "amount": i18n.Money(sf.Amount)})}
	if more := len(sfs) - 1; more > 0 {
		sentences = append(sentences, msg("insight.shortfall.more", args{"count": i18n.Int(int64(more))}))
	}
	var ok, last []i18n.Msg
	for _, o := range sf.Options {
		a := args{"name": i18n.Text(o.Name), "amount": i18n.Money(o.Available)}
		switch o.Kind {
		case PotEmergency:
			last = append(last, msg("insight.shortfall.pot", a))
		case PotSinking:
			ok = append(ok, msg("insight.shortfall.sinking_pot", a))
		default:
			ok = append(ok, msg("insight.shortfall.pot", a))
		}
	}
	if len(ok) > 0 {
		sentences = append(sentences, msg("insight.shortfall.cover", args{"pots": i18n.Nested(joinWith("list.or", ok))}))
	}
	if len(last) > 0 {
		sentences = append(sentences, msg("insight.shortfall.last_resort", args{"pots": i18n.Nested(joinWith("list.and", last))}))
	}
	if len(sf.Options) == 0 {
		sentences = append(sentences, msg("insight.shortfall.no_pots", nil))
	}
	sentences = append(sentences, msg("insight.shortfall.owed", nil))
	out := []Insight{{ID: "shortfall", Tone: ToneWarn, Msg: msg("insight.sentences", args{"items": i18n.JoinedList(sentences, " ")})}}
	if last := pr.Last(); last.Overdraft.IsPositive() {
		a := args{"month": i18n.Month(last.Key), "amount": i18n.Money(last.Overdraft)}
		out = append(out, Insight{ID: "overdraft_end", Tone: ToneWarn, Msg: msg("insight.overdraft_end", a)})
	}
	return out
}

// ruleWithdrawalCut flags planned withdrawals the pot couldn't fully pay.
func ruleWithdrawalCut(pr *Projection) []Insight {
	p := pr.Plan
	names := map[string]PlanWithdrawal{}
	for _, wd := range p.Withdrawals {
		names[wd.ID] = wd
	}
	items := map[string]PlanItem{}
	for _, l := range p.Items {
		items[l.ID] = l
	}
	for _, mo := range pr.Months {
		for _, pc := range mo.PotCosts {
			if pc.Paid.Cmp(pc.Cost) < 0 {
				l := items[pc.ItemID]
				a := args{
					"name":  i18n.Text(l.Name),
					"pot":   i18n.Text(p.Pots[l.PotIdx].Name),
					"held":  i18n.Money(pc.Paid),
					"month": i18n.Month(mo.Key),
					"rest":  i18n.Money(pc.Cost.Sub(pc.Paid)),
				}
				return []Insight{{ID: "pot_cost_cut_" + l.ID, Tone: ToneWarn, Msg: msg("insight.pot_cost_cut", a)}}
			}
		}
		for _, wm := range mo.Withdrawals {
			if wm.Taken.Cmp(wm.Planned) < 0 {
				wd := names[wm.ID]
				a := args{
					"name":    i18n.Text(wd.Name),
					"pot":     i18n.Text(p.Pots[wd.PotIdx].Name),
					"held":    i18n.Money(wm.Taken),
					"month":   i18n.Month(mo.Key),
					"planned": i18n.Money(wm.Planned),
				}
				return []Insight{{ID: "withdrawal_cut_" + wm.ID, Tone: ToneWarn, Msg: msg("insight.withdrawal_cut", a)}}
			}
		}
	}
	return nil
}

// ruleEmergencyFund suggests a buffer of three to six months of spending
// when there is no emergency pot.
func ruleEmergencyFund(pr *Projection) []Insight {
	p := pr.Plan
	for _, pt := range p.Pots {
		if pt.Kind == PotEmergency {
			return nil
		}
	}
	m := pr.Months[0]
	spend := m.Expenses.Add(m.DebtMinCost)
	if !spend.IsPositive() {
		return nil
	}
	a := args{"low": i18n.Money(spend.Mul(big.NewRat(3, 1), money.Ceil)), "high": i18n.Money(spend.Mul(big.NewRat(6, 1), money.Ceil))}
	return []Insight{{Tone: ToneInfo, Msg: msg("insight.emergency_fund", a)}}
}
