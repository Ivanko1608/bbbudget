package budget

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// shortBudget earns 1000 a month; the first month also has a 1300 bill.
func shortBudget() Budget {
	b := blank()
	b.Pots = []Pot{
		{ID: "savings", Name: "General savings", Kind: PotGeneral, Balance: d("200")},
		{ID: "holiday", Name: "Holiday fund", Kind: PotSinking, Balance: d("600")},
		{ID: "emergency", Name: "Emergency fund", Kind: PotEmergency, Balance: d("2000")},
	}
	b.Items = []Item{
		{ID: "pay", Name: "Pay", Amount: d("1000"), Currency: "EUR",
			Category: "income", Schedule: Monthly()},
		{ID: "rent", Name: "Rent", Amount: d("800"), Currency: "EUR",
			Category: "living", Schedule: Monthly()},
		{ID: "car", Name: "Car repair", Amount: d("500"), Currency: "EUR",
			Category: "living", Schedule: Once(0)},
	}
	return b
}

// The bug: a short month used to be paid from savings without saying so.
func TestShortMonthNeverSpendsSavings(t *testing.T) {
	r := mustReport(t, shortBudget())
	m0, m1 := r.Months[0], r.Months[1]
	eq(t, "m0 left", m0.Left, "-300.00")
	eq(t, "m0 short", m0.Short, "300.00")
	eq(t, "m0 owed", m0.Overdraft, "300.00")
	eq(t, "m0 savings untouched", m0.Savings, "2800.00")
	eq(t, "m0 to savings", m0.ToSavings, "0.00")
	// Next month's 200 surplus repays the overdraft first: nothing saved yet.
	eq(t, "m1 repaid", m1.OverdraftRepaid, "200.00")
	eq(t, "m1 owed", m1.Overdraft, "100.00")
	eq(t, "m1 saved", m1.Saved, "0.00")
	eq(t, "m2 saved", r.Months[2].Saved, "100.00")

	// Options: general first, then the sinking fund, emergency last.
	if len(r.Shortfalls) != 1 {
		t.Fatalf("shortfalls: %+v", r.Shortfalls)
	}
	expectEqual(t, "shortfall label", r.Shortfalls[0].Label, "Oct 2026")
	var order []string
	for _, o := range r.Shortfalls[0].Options {
		order = append(order, o.PotID+"="+o.Covers.String())
	}
	if strings.Join(order, " ") != "savings=200.00 holiday=300.00 emergency=300.00" {
		t.Errorf("options: %v", order)
	}
	shortfall := insight(r, "shortfall")
	if shortfall == nil {
		t.Fatal("no shortfall insight")
	}
	for _, want := range []string{
		"**Oct 2026 is €300 short.**",
		"Nothing is taken from your savings automatically",
		"General savings (€200 there) or Holiday fund (€600 there), " +
			"which delays what it's saved for",
		"Emergency fund (€2,000 there) is a last resort",
	} {
		expectContains(t, "shortfall insight", shortfall.Text, want)
	}
}

func hasInsightWithPrefix(r *Report, prefix string) bool {
	return slices.ContainsFunc(r.Insights, func(in Insight) bool {
		return strings.HasPrefix(in.ID, prefix)
	})
}

// Choosing to cover it is an explicit withdrawal, shown as from savings, not income.
func TestWithdrawalCoversShortMonth(t *testing.T) {
	cover := Withdrawal{Name: "Cover car repair", Pot: "general", Amount: d("300"),
		Schedule: Once(0)}
	b, err := Apply(shortBudget(), NewOp("add_withdrawal", "", cover))
	if err != nil {
		t.Fatal(err)
	}
	expectEqual(t, "withdrawal pot", b.Withdrawals[0].Pot, "savings")
	r := mustReport(t, b)
	m0 := r.Months[0]
	eq(t, "income unchanged", m0.Income, "1000.00")
	eq(t, "from savings", m0.FromSavings, "200.00") // the pot only holds 200
	eq(t, "still short", m0.Short, "100.00")
	eq(t, "general pot", m0.Pots[0].Balance, "0.00")
	if !hasInsightWithPrefix(r, "withdrawal_cut_") {
		t.Error("a withdrawal larger than the pot must be flagged")
	}
}

// Leftover: fixed contributions, then the debt share, then targets in order, then general.
func TestSurplusOrder(t *testing.T) {
	b := blank()
	b.ExtraToDebtPct = d("50")
	b.Pots = []Pot{
		{ID: "savings", Name: "General savings", Kind: PotGeneral, Balance: d("0")},
		{ID: "car", Name: "Car", Kind: PotSinking, Balance: d("0"), Monthly: d("100")},
		{ID: "emergency", Name: "Emergency fund", Kind: PotEmergency, Balance: d("950"),
			Target: d("1000")},
	}
	b.Items = []Item{{ID: "pay", Name: "Pay", Amount: d("500"), Currency: "EUR",
		Category: "income", Schedule: Monthly()}}
	b.Debts = []Debt{{ID: "card", Name: "Card", Currency: "EUR", Balance: d("10000"),
		APRPct: d("0"), MinPayment: d("0")}}
	m0 := mustReport(t, b).Months[0]
	// 500: car 100, debt 50% of 400 = 200, emergency fills its last 50, general gets 150.
	eq(t, "car", m0.Pots[1].In, "100.00")
	eq(t, "debt", m0.ExtraCost, "200.00")
	eq(t, "emergency", m0.Pots[2].In, "50.00")
	eq(t, "general", m0.Pots[0].In, "150.00")
}

// Old goals each counted the same money; as pots each fills on its own.
func TestLegacySavingsAndGoalsBecomePots(t *testing.T) {
	b, err := ParseDocument([]byte(`
name: Old
base_currency: EUR
start_month: "2026-10"
savings_start: 500
categories: [{id: income, label: Income, kind: income}]
items: [{name: Pay, amount: 1000, category: income}]
goals:
  - {name: Emergency fund, target: 1000}
  - {name: Laptop, target: 1000}
`), "yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Pots) != 3 {
		t.Fatalf("pots: %+v", b.Pots)
	}
	expectEqual(t, "general balance", b.Pots[0].Balance.String(), "500")
	expectEqual(t, "emergency kind", b.Pots[1].Kind, PotEmergency)
	expectEqual(t, "laptop kind", b.Pots[2].Kind, PotSinking)
	r := mustReport(t, b)
	// Before, both goals were "reached" by the same €1,000. Now the emergency
	// fund fills first (Oct), then the laptop (Nov).
	expectEqual(t, "emergency fund reached", r.Goals[0].ReachedLabel, "Oct 2026")
	expectEqual(t, "laptop reached", r.Goals[1].ReachedLabel, "Nov 2026")
	// Saved documents use pots only.
	saved := string(must(Clone(b)))
	expectNotContains(t, "saved document", saved, "savings_start")
	expectNotContains(t, "saved document", saved, "goals")
	// A negative old balance is money owed, not a negative pot.
	negative := "name: Old\nbase_currency: EUR\nstart_month: \"2026-10\"\nsavings_start: -150\n"
	b2, err := ParseDocument([]byte(negative), "yaml")
	if err != nil {
		t.Fatalf("negative savings: %v", err)
	}
	expectEqual(t, "overdraft start", b2.OverdraftStart.String(), "150")
	expectEqual(t, "general balance", b2.Pots[0].Balance.String(), "0")
	// Both forms at once is ambiguous.
	both := "name: X\nsavings_start: 5\npots: [{name: A}]\n"
	if _, err := ParseDocument([]byte(both), "yaml"); err == nil {
		t.Error("savings_start alongside pots must be rejected")
	}
}

func TestPotRules(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T) error
		want string
	}{
		{
			name: "removing a pot with a withdrawal",
			run: func(t *testing.T) error {
				trip := Withdrawal{Name: "Trip", Pot: "holiday", Amount: d("100"),
					Schedule: Once(3)}
				b := mustApply(t, shortBudget(), NewOp("add_withdrawal", "", trip))
				_, err := Apply(b, NewOp("remove_pot", "holiday", nil))
				return err
			},
			want: "Trip",
		},
		{
			name: "no pots",
			run: func(t *testing.T) error {
				none := shortBudget()
				none.Pots = []Pot{}
				_, err := Compile(none)
				return err
			},
			want: "at least one savings pot",
		},
		{
			name: "negative pot",
			run: func(t *testing.T) error {
				negative := shortBudget()
				negative.Pots[0].Balance = d("-1")
				_, err := Compile(negative)
				return err
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.run(t)
			if err == nil {
				t.Fatal("accepted")
			}
			expectContains(t, "error", err.Error(), c.want)
		})
	}
}

func TestSavingsStartEditsDefaultPot(t *testing.T) {
	patch := SettingsPatch{SavingsStart: decp("750")}
	nb, err := Apply(shortBudget(), NewOp("set_settings", "", patch))
	if err != nil {
		t.Fatal(err)
	}
	expectEqual(t, "general balance", nb.Pots[0].Balance.String(), "750")
}

func TestEmergencyFundHint(t *testing.T) {
	b := shortBudget()
	b.Pots = b.Pots[:2]
	hint := insight(mustReport(t, b), "emergency_fund")
	if hint == nil {
		t.Fatal("no emergency fund hint")
	}
	// Spending in the first month is 1300: three to six months is 3900 to 7800.
	expectContains(t, "hint", hint.Text, "€3,900 to €7,800")
}

func must(b Budget, err error) []byte {
	if err != nil {
		panic(err)
	}
	raw, err := json.Marshal(b)
	if err != nil {
		panic(err)
	}
	return raw
}

// A windfall routed to a pot goes there whole: no debt share, no target filling.
func TestIncomeRoutedToPotSkipsLeftoverRules(t *testing.T) {
	b := blank()
	b.ExtraToDebtPct = d("50")
	b.Pots = []Pot{
		{ID: "savings", Name: "General savings", Kind: PotGeneral, Balance: d("0")},
		{ID: "move", Name: "Move fund", Kind: PotSinking, Balance: d("0"), Target: d("2000")},
	}
	b.Items = []Item{
		{ID: "pay", Name: "Pay", Amount: d("1000"), Currency: "EUR",
			Category: "income", Schedule: Monthly()},
		{ID: "rent", Name: "Rent", Amount: d("800"), Currency: "EUR",
			Category: "living", Schedule: Monthly()},
		{ID: "sale", Name: "Car sale", Amount: d("10000"), Currency: "EUR",
			Category: "income", Schedule: Once(1), Pot: "savings"},
	}
	b.Debts = []Debt{{ID: "card", Name: "Card", Currency: "EUR", Balance: d("5000"),
		APRPct: d("0"), MinPayment: d("0")}}
	m1 := mustReport(t, b).Months[1]
	eq(t, "income", m1.Income, "11000.00") // still income
	eq(t, "routed", m1.IncomeToPots, "10000.00")
	eq(t, "left", m1.Left, "200.00") // only the month's own money
	eq(t, "debt share of 200", m1.ExtraCost, "100.00")
	eq(t, "general", m1.Pots[0].In, "10000.00")
	eq(t, "move fund", m1.Pots[1].In, "100.00")
}

// A cost routed to a pot is paid from it as far as it holds; the rest hits the month.
func TestCostPaidFromPot(t *testing.T) {
	b := shortBudget()
	b.Items[2].Pot = "holiday" // the 500 car repair, from a pot holding 600
	r := mustReport(t, b)
	m0 := r.Months[0]
	eq(t, "paid from pot", m0.PaidFromPots, "500.00")
	eq(t, "left", m0.Left, "200.00")
	eq(t, "short", m0.Short, "0.00")
	// 600 − 500; the 200 leftover goes to General savings
	eq(t, "holiday", m0.Pots[1].Balance, "100.00")

	b.Items[2].Amount = d("900") // more than the pot holds
	r = mustReport(t, b)
	eq(t, "paid capped", r.Months[0].PaidFromPots, "600.00")
	eq(t, "rest is short", r.Months[0].Short, "100.00")
	if insight(r, "pot_cost_cut_car") == nil {
		t.Error("a cost bigger than its pot must be flagged")
	}
	_, err := Apply(b, NewOp("remove_pot", "holiday", nil))
	expectErrorContaining(t, "removing a pot an item uses", err, "Car repair")

	nb, err := Apply(b, NewOp("set_item", "car", ItemPatch{Pot: strp("General")}))
	if err != nil {
		t.Fatalf("route by name: %v", err)
	}
	expectEqual(t, "routed pot", nb.Items[2].Pot, "savings")

	nb, err = Apply(nb, NewOp("set_item", "car", ItemPatch{Pot: strp("")}))
	if err != nil {
		t.Fatalf("unroute: %v", err)
	}
	expectEqual(t, "unrouted pot", nb.Items[2].Pot, "")
}
