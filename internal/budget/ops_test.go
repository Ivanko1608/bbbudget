package budget

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"bbbudget/internal/money"
)

func strp(s string) *string { return &s }
func decp(s string) *money.Decimal {
	v := money.MustDecimal(s)
	return &v
}

func itemByID(b Budget, id string) Item {
	for _, l := range b.Items {
		if l.ID == id {
			return l
		}
	}
	return Item{}
}

func TestApplyDoesNotMutateInput(t *testing.T) {
	b := fixture(t)
	before, _ := json.Marshal(b)
	nb, err := Apply(b,
		NewOp("set_item", "rent", ItemPatch{Amount: decp("1200")}),
		NewOp("remove_item", "holiday", nil),
		NewOp("set_currency", "USD", CurrencyPatch{PerBase: decp("1.1")}),
	)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(b)
	if string(before) != string(after) {
		t.Fatal("Apply mutated its input")
	}
	if itemByID(nb, "rent").Amount.String() != "1200" || len(nb.Items) != len(b.Items)-1 || nb.Currencies[1].PerBase.String() != "1.1" {
		t.Fatalf("ops not applied: %+v", nb.Items[1])
	}
}

func TestFindIsStrictAboutAmbiguity(t *testing.T) {
	b := fixture(t)
	_, err := Apply(b, NewOp("set_item", "school", ItemPatch{Amount: decp("1")}))
	if err == nil || !strings.Contains(err.Error(), "matches several") {
		t.Fatalf("ambiguous ref should fail, got %v", err)
	}
	for _, ref := range []string{"supplies", "School supplies", "SCHOOL SUPPLIES", "suppl"} {
		nb, err := Apply(b, NewOp("set_item", ref, ItemPatch{Amount: decp("60")}))
		if err != nil {
			t.Fatalf("%q: %v", ref, err)
		}
		if itemByID(nb, "supplies").Amount.String() != "60" {
			t.Fatalf("%q edited the wrong item", ref)
		}
	}
	if _, err := Apply(b, NewOp("remove_item", "nothing like it", nil)); err == nil {
		t.Fatal("missing ref should fail")
	}
}

func TestAddItemDefaults(t *testing.T) {
	b := fixture(t)
	nb, err := Apply(b,
		NewOp("add_item", "", map[string]any{"name": "Gym", "amount": "35", "category": "home"}),
		NewOp("add_item", "", map[string]any{"name": "Gym", "amount": "100", "category": "home", "schedule": map[string]int{"start": 3, "every": 0}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	g1, g2 := nb.Items[len(nb.Items)-2], nb.Items[len(nb.Items)-1]
	if g1.ID != "gym" || g1.Currency != "EUR" || g1.Schedule != Monthly() {
		t.Errorf("defaults: %+v", g1)
	}
	if g2.ID != "gym-2" || g2.Schedule != Once(3) {
		t.Errorf("explicit once: %+v", g2)
	}
}

func TestOpsRejectBadInput(t *testing.T) {
	b := fixture(t)
	bad := []Op{
		NewOp("set_item", "rent", map[string]any{"amout": "1"}),                // typo'd field
		NewOp("set_item", "rent", ItemPatch{Amount: decp("1.001")}),            // sub-cent CHF
		NewOp("set_item", "rent", ItemPatch{Currency: strp("XYZ")}),            // unknown currency
		NewOp("set_currency", "GBP", CurrencyPatch{TransferFeePct: decp("1")}), // new currency, no rate
		NewOp("remove_currency", "USD", nil),                                   // in use
		NewOp("remove_category", "home", nil),                                  // in use
		NewOp("set_settings", "", SettingsPatch{DebtStrategy: strp("yolo")}),
		{Op: "explode"},
	}
	for _, op := range bad {
		if _, err := Apply(b, op); err == nil {
			t.Errorf("%s %s %s should fail", op.Op, op.Target, op.Value)
		}
	}
}

func TestAtomicity(t *testing.T) {
	b := fixture(t)
	_, err := Apply(b, NewOp("set_item", "rent", ItemPatch{Amount: decp("1")}), NewOp("remove_item", "nope", nil))
	var oe *OpError
	if err == nil || !strings.HasPrefix(err.Error(), "op 2 (remove_item)") || !errors.As(err, &oe) {
		t.Fatalf("got %v", err)
	}
	if itemByID(b, "rent").Amount.String() != "1150" {
		t.Fatal("partial apply leaked")
	}
}

func TestDebtAndGoalOps(t *testing.T) {
	b := fixture(t)
	nb, err := Apply(b,
		NewOp("add_debt", "", Debt{Name: "Credit card", Currency: "USD", Balance: d("4000"), APRPct: d("24"), MinPayment: d("250")}),
		NewOp("remove_item", "card-minimums", nil),
		NewOp("set_debt", "credit", DebtPatch{APRPct: decp("19.9")}),
		NewOp("add_goal", "", Goal{Name: "New laptop", Target: d("1500")}),
		NewOp("set_goal", "emergency", GoalPatch{Target: decp("3000")}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if nb.Debts[0].ID != "credit-card" || nb.Debts[0].APRPct.String() != "19.9" {
		t.Errorf("debt: %+v", nb.Debts[0])
	}
	if nb.Goals[1].ID != "new-laptop" || nb.Goals[0].Target.String() != "3000" {
		t.Errorf("goals: %+v", nb.Goals)
	}
}

func TestScenarios(t *testing.T) {
	b := fixture(t)
	b, s, err := AddScenario(b, "Dollar at 1.00", "weaker euro", []Op{NewOp("set_currency", "USD", CurrencyPatch{PerBase: decp("1.00")})})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err = AddScenario(b, "Cheaper school", "", []Op{NewOp("set_item", "tuition", ItemPatch{Amount: decp("450")})})
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "dollar-at-1-00" {
		t.Errorf("id %s", s.ID)
	}
	if _, _, err := AddScenario(b, "Broken", "", []Op{NewOp("remove_item", "ghost", nil)}); err == nil {
		t.Error("scenario with a bad op was accepted")
	}

	out, err := Compare(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 || out[0].ID != "baseline" {
		t.Fatalf("outcomes: %+v", out)
	}
	if out[1].DeltaEndSavings.Sign() >= 0 {
		t.Errorf("a stronger dollar should cost money: %s", out[1].DeltaEndSavings)
	}
	// 450 USD/month less at 1.08 and 2%: 450×1.02/1.08 = 425.00 × 12 = 5100.00
	if got := out[2].DeltaEndSavings.String(); got != "5100.00" {
		t.Errorf("cut delta = %s", got)
	}

	// A scenario whose target disappears reports an error instead of failing the comparison.
	b2, _ := Apply(b, NewOp("remove_item", "tuition", nil))
	out, err = Compare(b2, nil)
	if err != nil || out[2].Error == "" {
		t.Errorf("stale scenario: %+v %v", out, err)
	}

	adopted, err := AdoptScenario(b, "cheaper")
	if err != nil {
		t.Fatal(err)
	}
	if itemByID(adopted, "tuition").Amount.String() != "450" || len(adopted.Scenarios) != 1 {
		t.Errorf("adopt: %+v", adopted.Scenarios)
	}
	removed, err := RemoveScenario(b, "dollar-at-1-00")
	if err != nil || len(removed.Scenarios) != 1 || !reflect.DeepEqual(removed.Items, b.Items) {
		t.Errorf("remove: %v", err)
	}
}

func TestBudgetJSONRoundTrip(t *testing.T) {
	b := fixture(t)
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"amount":"45.5"`) || !strings.Contains(string(raw), `"per_base":"1.08"`) {
		t.Fatalf("amounts must serialise as exact strings: %s", raw)
	}
	var back Budget
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	r1, r2 := mustReport(t, b), mustReport(t, back)
	if r1.Totals != r2.Totals {
		t.Fatal("round trip changed the numbers")
	}
}
