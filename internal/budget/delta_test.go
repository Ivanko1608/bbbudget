package budget

import "testing"

func analyzed(t *testing.T, b Budget) *Report {
	t.Helper()
	r, err := Analyze(b)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDiffOnlyReportsWhatMoved(t *testing.T) {
	b := fixture(t)
	before := analyzed(t, b)
	if d := Diff(before, analyzed(t, b)); !d.Empty() {
		t.Fatalf("no change, but: %+v", d)
	}
	nb, err := Apply(b, NewOp("set_item", "rent", ItemPatch{Amount: decp("1200")}))
	if err != nil {
		t.Fatal(err)
	}
	d := Diff(before, analyzed(t, nb))
	end, ok := d.Totals["end_savings"]
	if !ok || end.Change.String() != end.After.Sub(end.Before).String() || !end.Change.IsNegative() {
		t.Fatalf("end savings: %+v", d.Totals)
	}
	if _, ok := d.Totals["income"]; ok {
		t.Error("income didn't change and shouldn't be reported")
	}
	if _, ok := d.FirstMonth["left"]; !ok {
		t.Error("first month left moved")
	}
	if d.Months != nil {
		t.Error("window length didn't change")
	}
}

func TestDiffInsightsAndDates(t *testing.T) {
	b := fixture(t)
	b.Debts = append(b.Debts, Debt{ID: "loan", Name: "Loan", Currency: "EUR", Balance: d("900"), APRPct: d("12"), MinPayment: d("100")})
	before := analyzed(t, b)
	nb, err := Apply(b, NewOp("set_debt", "loan", DebtPatch{MinPayment: decp("300")})) // pays off sooner
	if err != nil {
		t.Fatal(err)
	}
	d := Diff(before, analyzed(t, nb))
	if dates, ok := d.Debts["Loan"]; !ok || dates[0] == dates[1] {
		t.Fatalf("loan payoff should move: %+v", d.Debts)
	}
	if len(d.Changed) == 0 {
		t.Error("the loan's insights should change")
	}

	more := 24
	nb, err = Apply(b, NewOp("set_settings", "", SettingsPatch{Months: &more}))
	if err != nil {
		t.Fatal(err)
	}
	if d := Diff(before, analyzed(t, nb)); d.Months == nil || d.Months[1] != 24 {
		t.Fatalf("window change: %+v", d.Months)
	}
}
