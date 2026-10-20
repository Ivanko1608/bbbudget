package budget

import (
	"errors"
	"fmt"
	"math/big"
	"math/rand/v2"
	"os"
	"strings"
	"testing"
	"time"

	"bbbudget/internal/money"
)

var oct2026 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

var d = money.MustDecimal

// fixture loads the fictional built-in example template, starting Oct 2026.
func fixture(t *testing.T) Budget {
	t.Helper()
	raw, err := os.ReadFile("../templates/builtin/example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseDocument(raw, "yaml")
	if err != nil {
		t.Fatal(err)
	}
	b.StartMonth = "2026-10"
	if _, err := Compile(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// blank is a minimal valid budget: one income and one expense category.
func blank() Budget {
	b := Budget{
		Name: "Test", BaseCurrency: "EUR", StartMonth: "2026-10", Months: 12,
		ExtraToDebtPct: d("0"),
		Pots: []Pot{
			{ID: "savings", Name: "General savings", Kind: PotGeneral, Balance: d("0")},
		},
		Categories: []Category{
			{ID: "income", Label: "Income", Kind: KindIncome},
			{ID: "living", Label: "Living", Kind: KindExpense},
		},
	}
	Normalize(&b)
	return b
}

func mustReport(t *testing.T, b Budget) *Report {
	t.Helper()
	r, err := Analyze(b)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func eqMoney(t *testing.T, what string, got money.Money, want string) {
	t.Helper()
	if got.String() != want {
		t.Errorf("%s = %s, want %s", what, got, want)
	}
}

func eq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func contains(t *testing.T, what, text, want string) {
	t.Helper()
	if !strings.Contains(text, want) {
		t.Errorf("%s lacks %q: %s", what, want, text)
	}
}

func notContains(t *testing.T, what, text, unwanted string) {
	t.Helper()
	if strings.Contains(text, unwanted) {
		t.Errorf("%s has %q: %s", what, unwanted, text)
	}
}

func errorContains(t *testing.T, what string, err error, want string) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: no error, want one containing %q", what, want)
		return
	}
	contains(t, what, err.Error(), want)
}

// Hand-computed with the rounding policy (costs round up to the cent):
//
//	rent     1150 CHF / 0.94           = 1223.4042… -> 1223.41
//	tuition   900 USD / 1.08 × 1.02    =  850.00
//	supplies 45.50 USD / 1.08 × 1.02   =   42.9722… ->   42.98
//	cards     250 USD / 1.08 × 1.02    =  236.1111… ->  236.12
//	recurring = 1223.41+80+550+850+42.98+236.12 = 2982.51
func TestFixtureGolden(t *testing.T) {
	r := mustReport(t, fixture(t))
	m0, m1 := r.Months[0], r.Months[1]
	eqMoney(t, "m0 income", m0.Income, "3900.00")
	eqMoney(t, "m0 expenses", m0.Expenses, "3582.51") // + 600 holiday
	eqMoney(t, "m0 left", m0.Left, "317.49")
	eqMoney(t, "m1 left", m1.Left, "917.49")
	eqMoney(t, "home", m0.ByCategory["home"], "1853.41")
	eqMoney(t, "education", m1.ByCategory["education"], "892.98")
	eqMoney(t, "end savings", r.Totals.EndSavings, "10409.88") // 317.49 + 11 × 917.49
	goal := r.Goals[0]
	if goal.ReachedMonth == nil {
		t.Error("goal never reached")
	} else {
		eq(t, "goal reached month", *goal.ReachedMonth, 2)
	}
	eq(t, "goal reached label", goal.ReachedLabel, "Dec 2026")
	eq(t, "first month label", m0.Label, "Oct 2026")
	eq(t, "last month key", r.Months[11].Key, "2027-09")
}

// A single EUR debt, worked by hand month by month.
func TestDebtPayoffGolden(t *testing.T) {
	b := blank()
	b.ExtraToDebtPct = d("50")
	b.Items = []Item{
		{ID: "pay", Name: "Pay", Amount: d("1000"), Currency: "EUR",
			Category: "income", Schedule: Monthly()},
		{ID: "live", Name: "Living", Amount: d("700"), Currency: "EUR",
			Category: "living", Schedule: Monthly()},
	}
	b.Debts = []Debt{{ID: "card", Name: "Card", Currency: "EUR", Balance: d("1000"),
		APRPct: d("12"), MinPayment: d("100")}}
	r := mustReport(t, b)

	// month: interest, balance after all payments, saved
	want := [][3]string{
		{"10.00", "810.00", "100.00"},
		{"8.10", "618.10", "100.00"},
		{"6.19", "424.29", "100.00"}, // 6.181 rounds up: interest charged to you
		{"4.25", "228.54", "100.00"},
		{"2.29", "30.83", "100.00"},
		{"0.31", "0.00", "268.86"}, // min payment capped at 31.14, nothing left for extra
		{"0.00", "0.00", "300.00"},
	}
	for i, w := range want {
		m := r.Months[i]
		eqMoney(t, fmt.Sprintf("m%d interest", i), m.Debts[0].Interest, w[0])
		eqMoney(t, fmt.Sprintf("m%d balance", i), m.Debts[0].Balance, w[1])
		eqMoney(t, fmt.Sprintf("m%d saved", i), m.Saved, w[2])
	}
	summary := r.Debts[0]
	if summary.PaidOffMonth == nil {
		t.Fatalf("not paid off: %+v", summary)
	}
	eq(t, "paid off month", *summary.PaidOffMonth, 5)
	eqMoney(t, "total interest", summary.TotalInterest, "31.14")
}

func TestForeignDebtExtraNeverOverspends(t *testing.T) {
	b := fixture(t)
	b.Debts = []Debt{{ID: "card", Name: "Card", Currency: "USD", Balance: d("4000"),
		APRPct: d("24"), MinPayment: d("250")}}
	b.Items = b.Items[:len(b.Items)-1] // the minimum payment is now modelled by the debt
	r := mustReport(t, b)
	m0 := r.Months[0]
	// interest 4000 × 24/1200 = 80.00 USD
	eqMoney(t, "interest", m0.Debts[0].Interest, "80.00")
	// min 250 USD costs 250/1.08×1.02 = 236.111 -> 236.12
	eqMoney(t, "min cost", m0.DebtMinCost, "236.12")
	// left = 3900 − 3346.39 − 236.12 = 317.49; extra budget = floor(158.745) = 158.74
	eqMoney(t, "left", m0.Left, "317.49")
	// 158.74 € buys floor(158.74 × 1.08 / 1.02) = floor(168.0776…) = 168.07 USD
	eqMoney(t, "extra paid", m0.Debts[0].Extra, "168.07")
	// whose cost is ceil(168.07/1.08×1.02) = ceil(158.7327…) = 158.74, within budget
	eqMoney(t, "extra cost", m0.ExtraCost, "158.74")
}

func TestScheduleActiveIn(t *testing.T) {
	end := 7
	cases := []struct {
		s    Schedule
		want string // months 0..9
	}{
		{Monthly(), "1111111111"},
		{Once(0), "1000000000"},
		{Once(3), "0001000000"},
		{Schedule{Start: 1, Every: 3}, "0100100100"},
		{Schedule{Start: 2, Every: 1, End: &end}, "0011111100"},
		{Schedule{Start: 0, Every: 12}, "1000000000"},
	}
	for _, c := range cases {
		var got strings.Builder
		for m := 0; m < 10; m++ {
			if c.s.ActiveIn(m) {
				got.WriteByte('1')
			} else {
				got.WriteByte('0')
			}
		}
		if got.String() != c.want {
			t.Errorf("%+v: got %s want %s", c.s, got.String(), c.want)
		}
	}
}

func TestStrategies(t *testing.T) {
	mk := func(strategy string) *Report {
		b := blank()
		b.DebtStrategy, b.ExtraToDebtPct = strategy, d("100")
		b.Items = []Item{{ID: "pay", Name: "Pay", Amount: d("500"), Currency: "EUR",
			Category: "income", Schedule: Monthly()}}
		b.Debts = []Debt{
			{ID: "big-cheap", Name: "Big cheap", Currency: "EUR", Balance: d("5000"),
				APRPct: d("5"), MinPayment: d("0")},
			{ID: "small-dear", Name: "Small dear", Currency: "EUR", Balance: d("300"),
				APRPct: d("1"), MinPayment: d("0")},
			{ID: "mid-dearest", Name: "Mid dearest", Currency: "EUR", Balance: d("1000"),
				APRPct: d("30"), MinPayment: d("0")},
		}
		return mustReport(t, b)
	}
	first := func(r *Report) string {
		for _, dm := range r.Months[0].Debts {
			if dm.Extra.IsPositive() {
				return dm.ID
			}
		}
		return ""
	}
	if got := first(mk(StrategyAvalanche)); got != "mid-dearest" {
		t.Errorf("avalanche paid %s first", got)
	}
	if got := first(mk(StrategySnowball)); got != "small-dear" {
		t.Errorf("snowball paid %s first", got)
	}
	if got := first(mk(StrategyListed)); got != "big-cheap" {
		t.Errorf("listed paid %s first", got)
	}
	// Avalanche must never pay more interest than snowball here.
	avalancheInterest := mk(StrategyAvalanche).Totals.TotalInterest
	if avalancheInterest.Cmp(mk(StrategySnowball).Totals.TotalInterest) > 0 {
		t.Error("avalanche paid more interest than snowball")
	}
}

// randomBudget builds a valid budget with awkward rates, fees and amounts.
func randomBudget(rng *rand.Rand) Budget {
	dec := func(maxInt, scale int) money.Decimal {
		v := big.NewRat(rng.Int64N(int64(maxInt)*100+1), 100)
		return money.MustDecimal(v.FloatString(scale))
	}
	rate := func() money.Decimal {
		return money.MustDecimal(big.NewRat(rng.Int64N(2_000_000)+1, 10_000).FloatString(4))
	}
	b := blank()
	b.Months = 1 + rng.IntN(36)
	b.Pots[0].Balance = dec(2000, 2)
	if rng.IntN(2) == 0 {
		b.OverdraftStart = dec(500, 2)
	}
	for i := range rng.IntN(3) {
		pt := Pot{ID: fmt.Sprintf("p%d", i), Name: "P", Kind: PotKinds[rng.IntN(3)],
			Balance: dec(1500, 2)}
		if rng.IntN(2) == 0 {
			pt.Target = dec(4000, 2)
		}
		if rng.IntN(2) == 0 {
			pt.Monthly = dec(300, 2)
		}
		b.Pots = append(b.Pots, pt)
	}
	for i := range rng.IntN(3) {
		schedule := Schedule{Start: rng.IntN(4), Every: rng.IntN(3)}
		b.Withdrawals = append(b.Withdrawals, Withdrawal{ID: fmt.Sprintf("w%d", i), Name: "W",
			Pot: b.Pots[rng.IntN(len(b.Pots))].ID, Amount: dec(900, 2), Schedule: schedule})
	}
	b.ExtraToDebtPct = money.DecimalFromInt(rng.Int64N(101))
	b.DebtStrategy = []string{StrategyAvalanche, StrategySnowball, StrategyListed}[rng.IntN(3)]
	b.Currencies = []CurrencyRate{
		{Code: "USD", PerBase: rate(), TransferFeePct: dec(10, 2)},
		{Code: "CHF", PerBase: rate(), TransferFeePct: dec(3, 2)},
		{Code: "JPY", PerBase: rate(), TransferFeePct: dec(2, 2)},
	}
	codes := []string{"EUR", "USD", "CHF", "JPY"}
	for i := range 1 + rng.IntN(12) {
		code := codes[rng.IntN(4)]
		amt := dec(5000, 2)
		if code == "JPY" {
			amt = money.DecimalFromInt(rng.Int64N(500000))
		}
		cat := "living"
		if rng.IntN(4) == 0 {
			cat = "income"
		}
		s := Schedule{Start: rng.IntN(4), Every: rng.IntN(4)}
		b.Items = append(b.Items, Item{ID: fmt.Sprintf("l%d", i), Name: "L", Amount: amt,
			Currency: code, Category: cat, Schedule: s})
	}
	for i := range b.Items {
		if rng.IntN(4) == 0 {
			b.Items[i].Pot = b.Pots[rng.IntN(len(b.Pots))].ID
		}
	}
	for i := range rng.IntN(4) {
		code := codes[rng.IntN(3)]
		b.Debts = append(b.Debts, Debt{ID: fmt.Sprintf("d%d", i), Name: "D", Currency: code,
			Balance: dec(15000, 2), APRPct: dec(60, 2), MinPayment: dec(700, 2)})
		if rng.IntN(2) == 0 {
			m := rng.IntN(b.Months + 2) // sometimes after the window
			b.Debts[i].PayoffMonth = &m
			if rng.IntN(3) > 0 {
				b.Debts[i].PayoffFrom = b.Pots[rng.IntN(len(b.Pots))].ID
			}
		}
	}
	return b
}

func TestInvariantsHoldExactly(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	var fromPot, partial, fromMonth int
	for n := range 400 {
		b := randomBudget(rng)
		p, err := Compile(b)
		if err != nil {
			t.Fatalf("case %d: %v", n, err)
		}
		pr, err := Project(p)
		if err != nil {
			t.Fatalf("case %d: %v", n, err)
		}
		prevSav := money.Zero(p.Base)
		prevPot := make([]money.Money, len(p.Pots))
		for k, pt := range p.Pots {
			prevSav, prevPot[k] = prevSav.Add(pt.Bal), pt.Bal
		}
		prevOwed := p.OverdraftStart
		prevBal := make([]money.Money, len(p.Debts))
		for i, d := range p.Debts {
			prevBal[i] = d.Bal
		}
		for _, m := range pr.Months {
			fail := func(format string, args ...any) {
				prefix := fmt.Sprintf("case %d month %d: ", n, m.Index)
				t.Fatalf(prefix+format, args...)
			}
			if m.Left.Cmp(leftFromOwnMoney(m)) != 0 {
				fail("left != income - routed income - (expenses - paid from pots) - min" +
					" - (payoff - payoff from pots)")
			}
			if !m.FromSavings.Sub(m.Transfers.Add(m.PaidFromPots).Add(m.PayoffFromPots)).IsZero() {
				fail("from savings != transfers + paid from pots + payoff from pots")
			}
			if m.PayoffFromPots.Cmp(m.Payoff) > 0 {
				fail("payoff from pots %s > payoff %s", m.PayoffFromPots, m.Payoff)
			}
			// Cash reconciles: what came in pays debts and savings, the rest is owed.
			cashIn := m.Left.Add(m.Transfers).Add(m.Overdraft.Sub(prevOwed))
			cashOut := m.ExtraCost.Add(m.ToSavings.Sub(m.IncomeToPots))
			if cashIn.Cmp(cashOut) != 0 {
				fail("cash does not reconcile")
			}
			if !m.Saved.Sub(m.ToSavings.Sub(m.FromSavings)).IsZero() {
				fail("saved != to - from")
			}
			if !m.Savings.Sub(prevSav.Add(m.Saved)).IsZero() {
				fail("savings not cumulative")
			}
			// Savings are never spent silently: a pot only goes down by a planned withdrawal.
			sum := money.Zero(p.Base)
			for k, pm := range m.Pots {
				if pm.Balance.IsNegative() {
					fail("pot %s negative", pm.ID)
				}
				if !prevPot[k].Add(pm.In).Sub(pm.Out).Sub(pm.Balance).IsZero() {
					fail("pot %s does not reconcile", pm.ID)
				}
				sum, prevPot[k] = sum.Add(pm.Balance), pm.Balance
			}
			if sum.Cmp(m.Savings) != 0 {
				fail("pots %s != savings %s", sum, m.Savings)
			}
			taken := money.Zero(p.Base)
			for _, wm := range m.Withdrawals {
				taken = taken.Add(wm.Taken)
			}
			if taken.Cmp(m.Transfers) != 0 {
				fail("withdrawals %s != transfers %s", taken, m.Transfers)
			}
			// A short month is owed. While anything is owed no leftover is saved or sent
			// to debts (income routed to a pot still goes there: that was the choice).
			if m.Overdraft.IsNegative() {
				fail("overdraft %s negative", m.Overdraft)
			}
			if spendsLeftoverWhileOwing(m) {
				saved := m.ToSavings.Sub(m.IncomeToPots)
				fail("saved %s / extra %s while %s is owed", saved, m.ExtraCost, m.Overdraft)
			}
			flow := m.Left.Add(m.Transfers)
			if flow.IsNegative() != m.Short.IsPositive() {
				fail("short %s disagrees with the sign of flow %s", m.Short, flow)
			}
			if flow.IsNegative() && m.Short.Cmp(flow.Neg()) != 0 {
				fail("short %s != shortfall of flow %s", m.Short, flow)
			}
			prevOwed = m.Overdraft
			cats := money.Zero(p.Base)
			for _, v := range m.ByCategory {
				cats = cats.Add(v)
			}
			if cats.Cmp(m.Expenses) != 0 {
				fail("categories %s != expenses %s", cats, m.Expenses)
			}
			if m.ExtraCost.IsNegative() {
				fail("extra %s negative", m.ExtraCost)
			}
			debtCost := money.Zero(p.Base)
			for i, dm := range m.Debts {
				debtCost = debtCost.Add(dm.Cost)
				if dm.Balance.IsNegative() {
					fail("negative balance")
				}
				owedAfter := prevBal[i].Add(dm.Interest).Sub(dm.MinPaid).Sub(dm.Extra)
				owedAfter = owedAfter.Sub(dm.Payoff)
				if owedAfter.Cmp(dm.Balance) != 0 {
					fail("debt %s does not reconcile", dm.ID)
				}
				// A payoff from the month's own money always clears the debt.
				debt := p.Debts[i]
				paysOffFromMonth := debt.PaysOffIn(m.Index) && debt.PotIdx < 0
				if paysOffFromMonth && dm.Balance.IsPositive() {
					fail("debt %s not cleared by its payoff", dm.ID)
				}
				if dm.Payoff.IsPositive() && !p.Debts[i].PaysOffIn(m.Index) {
					fail("debt %s paid off outside its payoff month", dm.ID)
				}
				if dm.Payoff.IsPositive() {
					switch {
					case p.Debts[i].PotIdx < 0:
						fromMonth++
					case dm.Balance.IsPositive():
						partial++
					default:
						fromPot++
					}
				}
				prevBal[i] = dm.Balance
			}
			if debtCost.Cmp(m.DebtPayments) != 0 {
				fail("debt costs %s != debt payments %s", debtCost, m.DebtPayments)
			}
			minPayoffExtra := m.DebtMinCost.Add(m.Payoff).Add(m.ExtraCost)
			if m.DebtPayments.Cmp(minPayoffExtra) != 0 {
				fail("debt payments %s != min+payoff+extra %s", m.DebtPayments, minPayoffExtra)
			}
			prevSav = m.Savings
		}
		if _, err := buildReport(pr); err != nil {
			t.Fatalf("case %d report: %v", n, err)
		}
	}
	if fromPot == 0 {
		t.Error("random budgets never paid off a debt in full from a pot")
	}
	if partial == 0 {
		t.Error("random budgets never paid off a debt in part from a pot")
	}
	if fromMonth == 0 {
		t.Error("random budgets never paid off a debt from the month")
	}
}

func leftFromOwnMoney(m Month) money.Money {
	income := m.Income.Sub(m.IncomeToPots)
	expenses := m.Expenses.Sub(m.PaidFromPots)
	payoff := m.Payoff.Sub(m.PayoffFromPots)
	return income.Sub(expenses).Sub(m.DebtMinCost).Sub(payoff)
}

func spendsLeftoverWhileOwing(m Month) bool {
	if !m.Overdraft.IsPositive() {
		return false
	}
	saved := m.ToSavings.Sub(m.IncomeToPots)
	return saved.IsPositive() || m.ExtraCost.IsPositive()
}

func TestOverflowIsAnErrorNotACrash(t *testing.T) {
	b := blank()
	b.Months = MaxMonths
	b.Currencies = []CurrencyRate{{Code: "JPY", PerBase: d("0.000000000001")}}
	for i := range 50 {
		b.Items = append(b.Items, Item{ID: fmt.Sprintf("l%d", i), Name: "x",
			Amount: d("1000000000000"), Currency: "JPY", Category: "living", Schedule: Monthly()})
	}
	_, err := Analyze(b)
	var ae *money.ArithmeticError
	if !errors.As(err, &ae) {
		t.Fatalf("expected an ArithmeticError, got %v", err)
	}
}

func TestValidationReportsEverything(t *testing.T) {
	b := blank()
	b.Name = ""
	b.Months = 0
	b.StartMonth = "2026-13"
	b.Items = []Item{
		{ID: "a", Name: "A", Amount: d("1.234"), Currency: "EUR",
			Category: "living", Schedule: Monthly()},
		{ID: "a", Name: "B", Amount: d("1"), Currency: "XXX", Category: "nope",
			Schedule: Monthly()},
		{ID: "c", Name: "C", Amount: d("-5"), Currency: "EUR", Category: "living",
			Schedule: Monthly()},
	}
	_, err := Compile(b)
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("got %v", err)
	}
	paths := map[string]bool{}
	for _, is := range ve.Issues {
		paths[is.Path] = true
	}
	wantPaths := []string{
		"name", "months", "start_month", "items[0].amount", "items[1].id",
		"items[1].currency", "items[1].category", "items[2].amount",
	}
	for _, want := range wantPaths {
		if !paths[want] {
			t.Errorf("missing issue for %s; got %v", want, ve.Issues)
		}
	}
}

func TestInsightsForFixture(t *testing.T) {
	r := mustReport(t, fixture(t))
	got := map[string]string{}
	for _, in := range r.Insights {
		got[in.ID] = in.Text
	}
	wantIDs := []string{
		"category_share", "transfer_loss_usd", "fx_risk_usd", "fx_risk_chf",
		"one_off_holiday", "first_month", "biggest_item", "goal_emergency",
	}
	for _, id := range wantIDs {
		if got[id] == "" {
			t.Errorf("missing insight %s; have %v", id, got)
		}
	}
	if _, ok := got["transfer_loss_chf"]; ok {
		t.Error("CHF has no fee, so no transfer-loss insight")
	}
	// 1195.50 USD/month at 2%: 1129.08 with the fee − 1106.94 without = 22.14
	if !strings.Contains(got["transfer_loss_usd"], "**€22**") {
		t.Errorf("transfer loss: %s", got["transfer_loss_usd"])
	}
	if !strings.Contains(got["first_month"], "**€317**") {
		t.Errorf("first month: %s", got["first_month"])
	}
}
