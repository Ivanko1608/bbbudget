package budget

import (
	"fmt"
	"math/big"
	"sort"
	"strings"

	"bbbudget/internal/money"
)

// Insight tones.
const (
	ToneInfo = "info"
	ToneWarn = "warn"
	ToneGood = "good"
)

// Insight is one plain-language observation. Text may contain **bold** spans.
type Insight struct {
	ID   string `json:"id"`
	Tone string `json:"tone"`
	Text string `json:"text"`
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
	{"goals", ruleGoals},
	{"negative_savings", ruleNegativeSavings},
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

func w(m money.Money) string { return m.Display() }

// fxRate renders an exchange rate with 4 significant digits, so that a crypto
// rate (0.00001352 BTC per €) doesn't print as 0.00.
func fxRate(r *big.Rat) string { return money.RoundSignificant(r, 4).String() }

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
	var parts []string
	for _, c := range p.Doc.Categories {
		if t, ok := totals[c.ID]; ok && t.IsPositive() {
			parts = append(parts, fmt.Sprintf("%s **%s** (%d%%)", c.Label, w(t), pct(t, inc)))
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return []Insight{{Tone: ToneInfo, Text: "Recurring costs per month: " + strings.Join(parts, ", ") + "."}}
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
		out = append(out, Insight{ID: "transfer_loss_" + strings.ToLower(code), Tone: tone, Text: fmt.Sprintf(
			"Sending %s a month loses about **%s** to fees and rates, **%s** a year. Each percentage point shaved off the transfer route saves %s a month.",
			w(amt), w(loss), w(loss.Mul(big.NewRat(12, 1), money.HalfEven)), w(perPoint))})
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
		out = append(out, Insight{ID: "fx_risk_" + strings.ToLower(code), Tone: ToneInfo, Text: fmt.Sprintf(
			"%s risk: if it strengthens 10%% (%s per %s), the %s side costs %s (**+%s**/month); if it weakens 10%% (%s), it drops to %s.",
			code, fxRate(strong), p.Base.Code, code, w(atStrong), w(atStrong.Sub(now)), fxRate(weak), w(atWeak))})
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
	text := fmt.Sprintf("**%d%%** of recurring income (%s a month) arrives in %s. If crypto falls 30%%, that is **%s** a month less",
		pct(total, p.RecurringIncome()), w(total), strings.Join(codes, ", "), w(drop))
	after := m.Left.Sub(drop)
	if after.IsNegative() {
		return []Insight{{Tone: ToneWarn, Text: text + fmt.Sprintf(", and %s would end **%s short**. Consider converting part of it as it arrives.", m.Label, w(after.Neg()))}}
	}
	return []Insight{{Tone: ToneInfo, Text: text + fmt.Sprintf("; %s would still end %s ahead.", m.Label, w(after))}}
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
		out = append(out, Insight{ID: "cash_out_loss_" + strings.ToLower(code), Tone: ToneInfo, Text: fmt.Sprintf(
			"Converting your %s income (%s a month) loses about **%s** a month to exchange fees and spread, %s a year.",
			code, w(amt), w(loss), w(loss.Mul(big.NewRat(12, 1), money.HalfEven)))})
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
		verb := "costs"
		amt := p.PayCost(l.Amt, roundPay)
		if l.Income {
			verb, amt = "brings in", p.IncomeValue(l.Amt, roundReceive)
		}
		out = append(out, Insight{ID: "one_off_" + l.ID, Tone: ToneInfo, Text: fmt.Sprintf(
			"One-off: **%s** %s %s in %s.", l.Name, verb, w(amt), p.MonthLabel(l.Schedule.Start))})
	}
	return out
}

func ruleFirstMonth(pr *Projection) []Insight {
	m := pr.Months[0]
	if m.Left.IsNegative() {
		return []Insight{{Tone: ToneWarn, Text: fmt.Sprintf("%s is **%s short**. Savings take the hit unless an item comes down.", m.Label, w(m.Left.Neg()))}}
	}
	inc := m.Income
	thin := !inc.IsPositive() || pct(m.Saved, inc) < 5
	day := m.Saved.Mul(big.NewRat(1, 30), money.Floor)
	text := fmt.Sprintf("After everything, **%s** goes to savings in %s: about %s a day of slack.", w(m.Saved), m.Label, w(day))
	if thin {
		return []Insight{{Tone: ToneWarn, Text: text + " That's thin: one surprise bill wipes it out."}}
	}
	return []Insight{{Tone: ToneGood, Text: text}}
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
	return []Insight{{Tone: ToneInfo, Text: fmt.Sprintf("Biggest recurring item: **%s** at %s a month (%d%% of income).",
		best.Name, w(bestAmt), pct(bestAmt, p.RecurringIncome()))}}
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
		if d.Min.Cmp(interest) <= 0 {
			out = append(out, Insight{ID: "debt_min_" + d.ID, Tone: ToneWarn, Text: fmt.Sprintf(
				"%s: the minimum payment (%s) doesn't cover the interest (%s a month). Without extra payments the balance grows.",
				d.Name, w(d.Min), w(interest))})
		} else {
			out = append(out, Insight{ID: "debt_min_" + d.ID, Tone: ToneInfo, Text: fmt.Sprintf(
				"%s: of the %s minimum, **%s** is interest this month; only %s reduces the balance.",
				d.Name, w(d.Min), w(interest), w(d.Min.Sub(interest)))})
		}
		when := "is **not paid off** in this window"
		tone := ToneWarn
		if s.PaidOffMonth != nil {
			when, tone = "is paid off in **"+s.PaidOffLabel+"**", ToneGood
		}
		out = append(out, Insight{ID: "debt_total_" + d.ID, Tone: tone, Text: fmt.Sprintf(
			"%s %s and costs %s (%s) in interest over %d months.",
			d.Name, when, w(s.TotalInterest), w(p.ToBase(s.TotalInterest, money.HalfEven)), p.Months)})
	}
	return out
}

func ruleGoals(pr *Projection) []Insight {
	var out []Insight
	for _, g := range pr.GoalStatuses() {
		switch {
		case g.AlreadyMet:
			out = append(out, Insight{ID: "goal_" + g.ID, Tone: ToneGood, Text: fmt.Sprintf("%s (%s) is already covered by current savings.", g.Name, w(g.Target))})
		case g.ReachedMonth != nil:
			out = append(out, Insight{ID: "goal_" + g.ID, Tone: ToneGood, Text: fmt.Sprintf("At this pace %s (%s) is reached in **%s**.", g.Name, w(g.Target), g.ReachedLabel)})
		default:
			out = append(out, Insight{ID: "goal_" + g.ID, Tone: ToneWarn, Text: fmt.Sprintf(
				"%s (%s) isn't reached within %d months. Getting there needs about **%s** saved per month.",
				g.Name, w(g.Target), pr.Plan.Months, w(*g.NeededPerMonth))})
		}
	}
	return out
}

func ruleNegativeSavings(pr *Projection) []Insight {
	for _, m := range pr.Months {
		if m.Savings.IsNegative() {
			return []Insight{{Tone: ToneWarn, Text: fmt.Sprintf("Savings go negative in **%s**. Something has to give before then.", m.Label)}}
		}
	}
	return nil
}
