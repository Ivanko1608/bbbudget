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

func edited(t *testing.T, b Budget, op Op) Budget {
	t.Helper()
	nb, err := Apply(b, op)
	if err != nil {
		t.Fatal(err)
	}
	return nb
}

func withLoan(t *testing.T) Budget {
	t.Helper()
	b := fixture(t)
	loan := Debt{
		ID:         "loan",
		Name:       "Loan",
		Currency:   "EUR",
		Balance:    d("900"),
		APRPct:     d("12"),
		MinPayment: d("100"),
	}
	b.Debts = append(b.Debts, loan)
	return b
}

func TestNoEditHasNoEffect(t *testing.T) {
	b := fixture(t)

	effect := EffectOf(analyzed(t, b), analyzed(t, b))

	if !effect.None() {
		t.Fatalf("nothing changed, but: %+v", effect)
	}
}

func TestEffectHoldsOnlyTheFiguresThatMoved(t *testing.T) {
	b := fixture(t)
	higherRent := edited(t, b, NewOp("set_item", "rent", ItemPatch{Amount: decp("1200")}))

	effect := EffectOf(analyzed(t, b), analyzed(t, higherRent))

	end := effect.Totals.EndSavings
	if end == nil {
		t.Fatal("end savings should move")
	}
	if want := end.After.Sub(end.Before); end.By.Cmp(want) != 0 {
		t.Errorf("end savings moved by %s, want %s", end.By, want)
	}
	if !end.By.IsNegative() {
		t.Errorf("higher rent should lower end savings, moved by %s", end.By)
	}
	if effect.Totals.Income != nil {
		t.Error("income didn't change and shouldn't be reported")
	}
	if effect.FirstMonth.Left == nil {
		t.Error("the first month's leftover should move")
	}
	if effect.Months != nil {
		t.Error("the projection length didn't change")
	}
}

func TestEffectMovesDebtPayoff(t *testing.T) {
	b := withLoan(t)
	paysMore := edited(t, b, NewOp("set_debt", "loan", DebtPatch{MinPayment: decp("300")}))

	effect := EffectOf(analyzed(t, b), analyzed(t, paysMore))

	payoff, moved := effect.DebtPayoffs["Loan"]
	if !moved {
		t.Fatalf("the loan's payoff should move: %+v", effect.DebtPayoffs)
	}
	if payoff.After >= payoff.Before {
		t.Errorf("paying more should pay off sooner: %s, was %s", payoff.After, payoff.Before)
	}
	if len(effect.Insights.Changed) == 0 {
		t.Error("the loan's insights should change")
	}
}

func TestEffectReportsALongerProjection(t *testing.T) {
	b := fixture(t)
	months := 24
	longer := edited(t, b, NewOp("set_settings", "", SettingsPatch{Months: &months}))

	effect := EffectOf(analyzed(t, b), analyzed(t, longer))

	if effect.Months == nil {
		t.Fatal("the projection length should change")
	}
	if effect.Months.After != 24 {
		t.Errorf("months after: %d", effect.Months.After)
	}
}
