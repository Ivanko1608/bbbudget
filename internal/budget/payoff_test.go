package budget

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// saleBudget earns 1000 and spends 800 a month. A 10000 sale in month 1 goes
// into General savings, and the card (1% a month, 100 minimum) is paid off
// from there in the same month.
func saleBudget() Budget {
	b := blank()
	b.ExtraToDebtPct = d("50")
	b.Items = []Item{
		{ID: "pay", Name: "Pay", Amount: d("1000"), Currency: "EUR",
			Category: "income", Schedule: Monthly()},
		{ID: "rent", Name: "Rent", Amount: d("800"), Currency: "EUR",
			Category: "living", Schedule: Monthly()},
		{ID: "sale", Name: "Car sale", Amount: d("10000"), Currency: "EUR",
			Category: "income", Schedule: Once(1), Pot: "savings"},
	}
	one := 1
	b.Debts = []Debt{{ID: "card", Name: "Card", Currency: "EUR", Balance: d("5000"),
		APRPct: d("12"), MinPayment: d("100"), PayoffMonth: &one, PayoffFrom: "savings"}}
	return b
}

func insight(r *Report, id string) *Insight {
	for i := range r.Insights {
		if r.Insights[i].ID == id {
			return &r.Insights[i]
		}
	}
	return nil
}

func hasInsightWithPrefix(r *Report, prefix string) bool {
	return slices.ContainsFunc(r.Insights, func(in Insight) bool {
		return strings.HasPrefix(in.ID, prefix)
	})
}

func TestPayoffFromPotAfterRoutedIncome(t *testing.T) {
	r := mustReport(t, saleBudget())
	m0, m1 := r.Months[0], r.Months[1]
	// Month 0 still pays the minimum and interest: 5000 + 50 − 100 − 50 extra.
	eqMoney(t, "m0 min", m0.DebtMinCost, "100.00")
	eqMoney(t, "m0 balance", m0.Debts[0].Balance, "4900.00")
	eqMoney(t, "m0 savings", m0.Savings, "50.00")

	// Month 1: 49 interest, then the whole 4949 from the pot the sale just landed in.
	eqMoney(t, "m1 interest", m1.Debts[0].Interest, "49.00")
	eqMoney(t, "m1 payoff", m1.Debts[0].Payoff, "4949.00")
	eqMoney(t, "m1 no minimum on top", m1.DebtMinCost, "0.00")
	eqMoney(t, "m1 balance", m1.Debts[0].Balance, "0.00")
	eqMoney(t, "m1 payoff cost", m1.Payoff, "4949.00")
	eqMoney(t, "m1 from pots", m1.PayoffFromPots, "4949.00")
	eqMoney(t, "m1 from savings", m1.FromSavings, "4949.00")
	eqMoney(t, "m1 debt payments", m1.DebtPayments, "4949.00")
	// The month's own money is untouched by the payoff, and with no debt left
	// the whole 200 is saved.
	eqMoney(t, "m1 left", m1.Left, "200.00")
	eqMoney(t, "m1 extra", m1.ExtraCost, "0.00")
	eqMoney(t, "m1 general", m1.Pots[0].Balance, "5301.00") // 50 + 10000 − 4949 + 200

	paidOff := r.Debts[0].PaidOffMonth
	if paidOff == nil {
		t.Fatal("debt not paid off")
	}
	eq(t, "paid off month", *paidOff, 1)
}

// A pot that holds less pays what it can; the rest stays owed and pays its minimum.
func TestPayoffFromPotThatFallsShort(t *testing.T) {
	b := saleBudget()
	b.Items[2].Amount = d("3000")
	r := mustReport(t, b)
	m1 := r.Months[1]
	eqMoney(t, "paid from pot", m1.Debts[0].Payoff, "3050.00") // all the pot holds: 50 + 3000
	eqMoney(t, "pot emptied", m1.PayoffFromPots, "3050.00")
	eqMoney(t, "minimum on the rest", m1.Debts[0].MinPaid, "100.00")
	eqMoney(t, "left", m1.Left, "100.00")
	eqMoney(t, "extra", m1.Debts[0].Extra, "50.00")
	eqMoney(t, "balance", m1.Debts[0].Balance, "1749.00") // 4949 − 3050 − 100 − 50
}

// An empty pot pays nothing, and the plan says so.
func TestPayoffFromEmptyPot(t *testing.T) {
	b := saleBudget()
	b.Items = b.Items[:2] // no sale
	b.Pots = append(b.Pots, Pot{ID: "move", Name: "Move fund", Kind: PotSinking, Balance: d("0")})
	b.Debts[0].PayoffFrom = "move"
	r := mustReport(t, b)
	eqMoney(t, "payoff", r.Months[1].Payoff, "0.00")
	eqMoney(t, "minimum still paid", r.Months[1].Debts[0].MinPaid, "100.00")
}

func TestPayoffInsights(t *testing.T) {
	cases := []struct {
		name   string
		budget func() Budget
		tone   Tone
		texts  []string
	}{
		{
			name:   "paid off from the pot the sale landed in",
			budget: saleBudget,
			tone:   ToneGood,
			texts:  []string{"**Nov 2026**", "General savings"},
		},
		{
			name: "a payoff the pot can't cover says what stays owed",
			budget: func() Budget {
				b := saleBudget()
				b.Items[2].Amount = d("3000")
				return b
			},
			tone:  ToneWarn,
			texts: []string{"€1,899"},
		},
		{
			name: "an empty pot is flagged",
			budget: func() Budget {
				b := saleBudget()
				b.Items = b.Items[:2] // no sale
				b.Pots = append(b.Pots,
					Pot{ID: "move", Name: "Move fund", Kind: PotSinking, Balance: d("0")})
				b.Debts[0].PayoffFrom = "move"
				return b
			},
			tone:  ToneWarn,
			texts: []string{"holds nothing"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := insight(mustReport(t, c.budget()), "debt_payoff_card")
			if in == nil {
				t.Fatal("no payoff insight")
			}
			eq(t, "tone", in.Tone, c.tone)
			for _, text := range c.texts {
				contains(t, "payoff insight", in.Text, text)
			}
		})
	}
}

// Without a pot the payoff comes out of the month: a short month is owed, savings untouched.
func TestPayoffFromTheMonth(t *testing.T) {
	b := saleBudget()
	b.Items = b.Items[:2]
	b.Debts[0].PayoffFrom = ""
	r := mustReport(t, b)
	m1 := r.Months[1]
	eqMoney(t, "payoff", m1.Payoff, "4949.00")
	eqMoney(t, "from pots", m1.PayoffFromPots, "0.00")
	eqMoney(t, "left", m1.Left, "-4749.00")
	eqMoney(t, "short", m1.Short, "4749.00")
	eqMoney(t, "savings untouched", m1.Savings, "50.00")
	if insight(r, "shortfall") == nil {
		t.Error("a payoff the month can't afford must show as a short month")
	}
}

// A payoff after the debt is already cleared, or after the window, does nothing.
func TestPayoffLateOrOutsideWindow(t *testing.T) {
	b := saleBudget()
	late := 40
	b.Debts[0].PayoffMonth = &late
	r := mustReport(t, b)
	for _, m := range r.Months {
		eqMoney(t, m.Label+" payoff", m.Payoff, "0.00")
	}
	if insight(r, "debt_payoff_card") != nil {
		t.Error("no payoff insight outside the window")
	}
}

func TestPayoffOps(t *testing.T) {
	b := saleBudget()
	b.Debts[0].PayoffMonth, b.Debts[0].PayoffFrom = nil, ""
	b.Pots = append(b.Pots, Pot{ID: "move", Name: "Move fund", Kind: PotSinking, Balance: d("0")})

	two := 2
	byPotName := DebtPatch{PayoffMonth: &two, PayoffFrom: strp("Move")}
	nb, err := Apply(b, NewOp("set_debt", "card", byPotName))
	if err != nil {
		t.Fatalf("set payoff by pot name: %v", err)
	}
	eq(t, "payoff pot", nb.Debts[0].PayoffFrom, "move")
	if nb.Debts[0].PayoffMonth == nil {
		t.Fatal("payoff month not set")
	}
	eq(t, "payoff month", *nb.Debts[0].PayoffMonth, 2)

	_, err = Apply(nb, NewOp("remove_pot", "move", nil))
	errorContains(t, "removing a pot a payoff uses", err, "Card")

	_, err = Apply(nb, NewOp("set_debt", "card", DebtPatch{PayoffFrom: strp("nowhere")}))
	if err == nil {
		t.Error("unknown pot must be refused")
	}

	off := -1
	nb, err = Apply(nb, NewOp("set_debt", "card", DebtPatch{PayoffMonth: &off}))
	if err != nil {
		t.Fatalf("remove payoff: %v", err)
	}
	if nb.Debts[0].PayoffMonth != nil {
		t.Errorf("payoff month = %d, want none", *nb.Debts[0].PayoffMonth)
	}
	eq(t, "payoff pot after removal", nb.Debts[0].PayoffFrom, "")

	loan := Debt{Name: "Loan", Balance: d("100"), APRPct: d("0"), MinPayment: d("10"),
		PayoffMonth: &two, PayoffFrom: "General"}
	nb, err = Apply(nb, NewOp("add_debt", "", loan))
	if err != nil {
		t.Fatalf("add debt with payoff: %v", err)
	}
	eq(t, "new debt payoff pot", nb.Debts[1].PayoffFrom, "savings")
}

func TestPayoffValidation(t *testing.T) {
	b := saleBudget()
	b.Debts[0].PayoffFrom = "nowhere"
	neg := -1
	b.Debts = append(b.Debts,
		Debt{ID: "loan", Name: "Loan", Currency: "EUR", Balance: d("1"), APRPct: d("0"),
			MinPayment: d("0"), PayoffMonth: &neg},
		Debt{ID: "car", Name: "Car loan", Currency: "EUR", Balance: d("1"), APRPct: d("0"),
			MinPayment: d("0"), PayoffFrom: "savings"})
	_, err := Compile(b)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want a validation error, got %v", err)
	}
	got := map[string]bool{}
	for _, is := range ve.Issues {
		got[is.Path] = true
	}
	wantPaths := []string{"debts[0].payoff_from", "debts[1].payoff_month", "debts[2].payoff_from"}
	for _, path := range wantPaths {
		if !got[path] {
			t.Errorf("missing issue at %s: %v", path, ve.Issues)
		}
	}
}

// A foreign debt costs its converted balance plus the transfer loss, and a pot
// that can't cover that pays exactly what it buys.
func TestPayoffInForeignCurrency(t *testing.T) {
	b := blank()
	b.Currencies = []CurrencyRate{{Code: "USD", PerBase: d("1.1"), TransferFeePct: d("2")}}
	b.Pots[0].Balance = d("2000")
	zero := 0
	b.Debts = []Debt{{ID: "loan", Name: "Loan", Currency: "USD", Balance: d("1100"),
		APRPct: d("0"), MinPayment: d("0"), PayoffMonth: &zero, PayoffFrom: "savings"}}
	m0 := mustReport(t, b).Months[0]
	eqMoney(t, "paid in USD", m0.Debts[0].Payoff, "1100.00")
	eqMoney(t, "cost: 1100 / 1.1 × 1.02", m0.Payoff, "1020.00")
	eqMoney(t, "pot", m0.Pots[0].Balance, "980.00")

	b.Pots[0].Balance = d("510")
	m0 = mustReport(t, b).Months[0]
	eqMoney(t, "what 510 buys: 510 × 1.1 / 1.02", m0.Debts[0].Payoff, "550.00")
	eqMoney(t, "cost never exceeds the pot", m0.PayoffFromPots, "510.00")
	eqMoney(t, "rest owed", m0.Debts[0].Balance, "550.00")
	eqMoney(t, "pot emptied", m0.Pots[0].Balance, "0.00")
}

// A payoff comes before planned transfers from the same pot that month, and a
// transfer that then finds the pot short is flagged.
func TestPayoffBeforeTransferFromSamePot(t *testing.T) {
	b := blank()
	b.Pots[0].Balance = d("1000")
	zero := 0
	b.Debts = []Debt{{ID: "card", Name: "Card", Currency: "EUR", Balance: d("600"),
		APRPct: d("0"), MinPayment: d("0"), PayoffMonth: &zero, PayoffFrom: "savings"}}
	b.Withdrawals = []Withdrawal{{ID: "tyres", Name: "Tyres", Pot: "savings",
		Amount: d("600"), Schedule: Once(0)}}
	r := mustReport(t, b)
	m0 := r.Months[0]
	eqMoney(t, "payoff", m0.Payoff, "600.00")
	eqMoney(t, "transfer gets the rest", m0.Transfers, "400.00")
	eqMoney(t, "from savings", m0.FromSavings, "1000.00")
	if insight(r, "withdrawal_cut_tyres") == nil {
		t.Error("a transfer the pot can't fully pay after a payoff must be flagged")
	}
}

// A payoff in a scenario is compared against the real budget like any edit.
func TestPayoffInScenario(t *testing.T) {
	b := saleBudget()
	b.Debts[0].PayoffMonth, b.Debts[0].PayoffFrom = nil, ""
	one := 1
	earlyPayoff := DebtPatch{PayoffMonth: &one, PayoffFrom: strp("General")}
	sc, err := Apply(b, NewOp("set_debt", "card", earlyPayoff))
	if err != nil {
		t.Fatal(err)
	}
	base, with := mustReport(t, b), mustReport(t, sc)
	if with.Totals.TotalInterest.Cmp(base.Totals.TotalInterest) >= 0 {
		t.Errorf("paying off early must cut interest: %s vs %s",
			with.Totals.TotalInterest, base.Totals.TotalInterest)
	}
}
