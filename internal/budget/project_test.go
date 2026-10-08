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
		SavingsStart: d("0"), ExtraToDebtPct: d("0"),
		Categories: []Category{{ID: "income", Label: "Income", Kind: KindIncome}, {ID: "living", Label: "Living", Kind: KindExpense}},
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

func eq(t *testing.T, what string, got money.Money, want string) {
	t.Helper()
	if got.String() != want {
		t.Errorf("%s = %s, want %s", what, got, want)
	}
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
	eq(t, "m0 income", m0.Income, "3900.00")
	eq(t, "m0 expenses", m0.Expenses, "3582.51") // + 600 holiday
	eq(t, "m0 left", m0.Left, "317.49")
	eq(t, "m1 left", m1.Left, "917.49")
	eq(t, "home", m0.ByCategory["home"], "1853.41")
	eq(t, "education", m1.ByCategory["education"], "892.98")
	eq(t, "end savings", r.Totals.EndSavings, "10409.88") // 317.49 + 11 × 917.49
	if g := r.Goals[0]; g.ReachedMonth == nil || *g.ReachedMonth != 2 || g.ReachedLabel != "Dec 2026" {
		t.Errorf("goal: %+v", g)
	}
	if m0.Label != "Oct 2026" || r.Months[11].Key != "2027-09" {
		t.Errorf("labels: %s %s", m0.Label, r.Months[11].Key)
	}
}

// A single EUR debt, worked by hand month by month.
func TestDebtPayoffGolden(t *testing.T) {
	b := blank()
	b.ExtraToDebtPct = d("50")
	b.Lines = []Line{
		{ID: "pay", Name: "Pay", Amount: d("1000"), Currency: "EUR", Category: "income", Schedule: Monthly()},
		{ID: "live", Name: "Living", Amount: d("700"), Currency: "EUR", Category: "living", Schedule: Monthly()},
	}
	b.Debts = []Debt{{ID: "card", Name: "Card", Currency: "EUR", Balance: d("1000"), APRPct: d("12"), MinPayment: d("100")}}
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
		eq(t, fmt.Sprintf("m%d interest", i), m.Debts[0].Interest, w[0])
		eq(t, fmt.Sprintf("m%d balance", i), m.Debts[0].Balance, w[1])
		eq(t, fmt.Sprintf("m%d saved", i), m.Saved, w[2])
	}
	s := r.Debts[0]
	if s.PaidOffMonth == nil || *s.PaidOffMonth != 5 {
		t.Fatalf("paid off: %+v", s)
	}
	eq(t, "total interest", s.TotalInterest, "31.14")
}

func TestForeignDebtExtraNeverOverspends(t *testing.T) {
	b := fixture(t)
	b.Debts = []Debt{{ID: "card", Name: "Card", Currency: "USD", Balance: d("4000"), APRPct: d("24"), MinPayment: d("250")}}
	b.Lines = b.Lines[:len(b.Lines)-1] // the minimum payment is now modelled by the debt
	r := mustReport(t, b)
	m0 := r.Months[0]
	// interest 4000 × 24/1200 = 80.00 USD
	eq(t, "interest", m0.Debts[0].Interest, "80.00")
	// min 250 USD costs 250/1.08×1.02 = 236.111 -> 236.12
	eq(t, "min cost", m0.DebtMinCost, "236.12")
	// left = 3900 − 3346.39 − 236.12 = 317.49; extra budget = floor(158.745) = 158.74
	eq(t, "left", m0.Left, "317.49")
	// 158.74 € buys floor(158.74 × 1.08 / 1.02) = floor(168.0776…) = 168.07 USD
	eq(t, "extra paid", m0.Debts[0].Extra, "168.07")
	// whose cost is ceil(168.07/1.08×1.02) = ceil(158.7327…) = 158.74, within budget
	eq(t, "extra cost", m0.ExtraCost, "158.74")
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
		b.Lines = []Line{{ID: "pay", Name: "Pay", Amount: d("500"), Currency: "EUR", Category: "income", Schedule: Monthly()}}
		b.Debts = []Debt{
			{ID: "big-cheap", Name: "Big cheap", Currency: "EUR", Balance: d("5000"), APRPct: d("5"), MinPayment: d("0")},
			{ID: "small-dear", Name: "Small dear", Currency: "EUR", Balance: d("300"), APRPct: d("1"), MinPayment: d("0")},
			{ID: "mid-dearest", Name: "Mid dearest", Currency: "EUR", Balance: d("1000"), APRPct: d("30"), MinPayment: d("0")},
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
	if mk(StrategyAvalanche).Totals.TotalInterest.Cmp(mk(StrategySnowball).Totals.TotalInterest) > 0 {
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
	b.SavingsStart = dec(2000, 2)
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
		b.Lines = append(b.Lines, Line{ID: fmt.Sprintf("l%d", i), Name: "L", Amount: amt, Currency: code, Category: cat, Schedule: s})
	}
	for i := range rng.IntN(4) {
		code := codes[rng.IntN(3)]
		b.Debts = append(b.Debts, Debt{ID: fmt.Sprintf("d%d", i), Name: "D", Currency: code,
			Balance: dec(15000, 2), APRPct: dec(60, 2), MinPayment: dec(700, 2)})
	}
	return b
}

func TestInvariantsHoldExactly(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
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
		prevSav := p.SavingsStart
		prevBal := make([]money.Money, len(p.Debts))
		for i, d := range p.Debts {
			prevBal[i] = d.Bal
		}
		for _, m := range pr.Months {
			fail := func(f string, a ...any) { t.Fatalf("case %d month %d: "+f, append([]any{n, m.Index}, a...)...) }
			if !m.Left.Sub(m.Income.Sub(m.Expenses).Sub(m.DebtMinCost)).IsZero() {
				fail("left != income - expenses - min")
			}
			if !m.Saved.Sub(m.Left.Sub(m.ExtraCost)).IsZero() {
				fail("saved != left - extra")
			}
			if !m.Savings.Sub(prevSav.Add(m.Saved)).IsZero() {
				fail("savings not cumulative")
			}
			cats := money.Zero(p.Base)
			for _, v := range m.ByCategory {
				cats = cats.Add(v)
			}
			if cats.Cmp(m.Expenses) != 0 {
				fail("categories %s != expenses %s", cats, m.Expenses)
			}
			if m.ExtraCost.IsNegative() || (!m.Left.IsPositive() && !m.ExtraCost.IsZero()) {
				fail("extra %s with left %s", m.ExtraCost, m.Left)
			}
			if m.Left.IsPositive() && m.ExtraCost.Cmp(m.Left.Percent(b.ExtraToDebtPct, money.Floor)) > 0 {
				fail("extra %s exceeds its budget", m.ExtraCost)
			}
			debtCost := money.Zero(p.Base)
			for i, dm := range m.Debts {
				debtCost = debtCost.Add(dm.Cost)
				if dm.Balance.IsNegative() {
					fail("negative balance")
				}
				if !prevBal[i].Add(dm.Interest).Sub(dm.MinPaid).Sub(dm.Extra).Sub(dm.Balance).IsZero() {
					fail("debt %s does not reconcile", dm.ID)
				}
				prevBal[i] = dm.Balance
			}
			if debtCost.Cmp(m.DebtMinCost.Add(m.ExtraCost)) != 0 {
				fail("debt costs %s != min+extra %s", debtCost, m.DebtMinCost.Add(m.ExtraCost))
			}
			prevSav = m.Savings
		}
		if _, err := buildReport(pr); err != nil {
			t.Fatalf("case %d report: %v", n, err)
		}
	}
}

func TestOverflowIsAnErrorNotACrash(t *testing.T) {
	b := blank()
	b.Months = MaxMonths
	b.Currencies = []CurrencyRate{{Code: "JPY", PerBase: d("0.000000000001")}}
	for i := range 50 {
		b.Lines = append(b.Lines, Line{ID: fmt.Sprintf("l%d", i), Name: "x", Amount: d("1000000000000"), Currency: "JPY", Category: "living", Schedule: Monthly()})
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
	b.Lines = []Line{
		{ID: "a", Name: "A", Amount: d("1.234"), Currency: "EUR", Category: "living", Schedule: Monthly()},
		{ID: "a", Name: "B", Amount: d("1"), Currency: "XXX", Category: "nope", Schedule: Monthly()},
		{ID: "c", Name: "C", Amount: d("-5"), Currency: "EUR", Category: "living", Schedule: Monthly()},
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
	for _, want := range []string{"name", "months", "start_month", "lines[0].amount", "lines[1].id", "lines[1].currency", "lines[1].category", "lines[2].amount"} {
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
	for _, id := range []string{"category_share", "transfer_loss_usd", "fx_risk_usd", "fx_risk_chf", "one_off_holiday", "first_month", "biggest_line", "goal_emergency"} {
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
