package budget

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"bbbudget/internal/money"
)

func strp(s string) *string                { return &s }
func rateSourcep(s RateSource) *RateSource { return &s }
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
	eq(t, "rent", itemByID(nb, "rent").Amount.String(), "1200")
	eq(t, "item count", len(nb.Items), len(b.Items)-1)
	eq(t, "USD rate", nb.Currencies[1].PerBase.String(), "1.1")
}

func TestFindIsStrictAboutAmbiguity(t *testing.T) {
	b := fixture(t)
	_, err := Apply(b, NewOp("set_item", "school", ItemPatch{Amount: decp("1")}))
	errorContains(t, "ambiguous ref", err, "matches several")
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
		NewOp("add_item", "", map[string]any{
			"name":     "Gym",
			"amount":   "100",
			"category": "home",
			"schedule": map[string]int{"start": 3, "every": 0},
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	g1, g2 := nb.Items[len(nb.Items)-2], nb.Items[len(nb.Items)-1]
	eq(t, "first id", g1.ID, "gym")
	eq(t, "first currency", g1.Currency, "EUR")
	eq(t, "first schedule", g1.Schedule, Monthly())
	eq(t, "second id", g2.ID, "gym-2")
	eq(t, "second schedule", g2.Schedule, Once(3))
}

func TestOpsRejectBadInput(t *testing.T) {
	b := fixture(t)
	bad := []Op{
		NewOp("set_item", "rent", map[string]any{"amout": "1"}),     // typo'd field
		NewOp("set_item", "rent", ItemPatch{Amount: decp("1.001")}), // sub-cent CHF
		NewOp("set_item", "rent", ItemPatch{Currency: strp("XYZ")}), // unknown currency
		// new currency, no rate
		NewOp("set_currency", "GBP", CurrencyPatch{TransferFeePct: decp("1")}),
		NewOp("remove_currency", "USD", nil),  // in use
		NewOp("remove_category", "home", nil), // in use
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
	_, err := Apply(b,
		NewOp("set_item", "rent", ItemPatch{Amount: decp("1")}),
		NewOp("remove_item", "nope", nil),
	)
	if err == nil {
		t.Fatal("a failing op was accepted")
	}
	if !strings.HasPrefix(err.Error(), "op 2 (remove_item)") {
		t.Errorf("error = %q, want it to name op 2 (remove_item)", err)
	}
	var oe *OpError
	if !errors.As(err, &oe) {
		t.Errorf("error %v is not an *OpError", err)
	}
	if itemByID(b, "rent").Amount.String() != "1150" {
		t.Fatal("partial apply leaked")
	}
}

func TestDebtAndGoalOps(t *testing.T) {
	b := fixture(t)
	nb, err := Apply(b,
		NewOp("add_debt", "", Debt{Name: "Credit card", Currency: "USD", Balance: d("4000"),
			APRPct: d("24"), MinPayment: d("250")}),
		NewOp("remove_item", "card-minimums", nil),
		NewOp("set_debt", "credit", DebtPatch{APRPct: decp("19.9")}),
		NewOp("add_goal", "", Goal{Name: "New laptop", Target: d("1500")}),
		NewOp("set_goal", "emergency", GoalPatch{Target: decp("3000")}),
	)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "debt id", nb.Debts[0].ID, "credit-card")
	eq(t, "debt rate", nb.Debts[0].APRPct.String(), "19.9")
	// The old goal ops still work, on pots.
	eq(t, "goal pot id", nb.Pots[2].ID, "new-laptop")
	eq(t, "goal pot kind", nb.Pots[2].Kind, PotSinking)
	eq(t, "emergency target", nb.Pots[1].Target.String(), "3000")
}

func TestScenarios(t *testing.T) {
	b := fixture(t)
	weakerEuro := []Op{NewOp("set_currency", "USD", CurrencyPatch{PerBase: decp("1.00")})}
	b, s, err := AddScenario(b, "Dollar at 1.00", "weaker euro", weakerEuro)
	if err != nil {
		t.Fatal(err)
	}
	cheaperSchool := []Op{NewOp("set_item", "tuition", ItemPatch{Amount: decp("450")})}
	b, _, err = AddScenario(b, "Cheaper school", "", cheaperSchool)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "dollar-at-1-00" {
		t.Errorf("id %s", s.ID)
	}
	ghost := []Op{NewOp("remove_item", "ghost", nil)}
	if _, _, err = AddScenario(b, "Broken", "", ghost); err == nil {
		t.Error("scenario with a bad op was accepted")
	}

	out, err := Compare(b, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("outcomes: %+v", out)
	}
	eq(t, "first outcome", out[0].ID, "baseline")
	if out[1].DeltaEndSavings.Sign() >= 0 {
		t.Errorf("a stronger dollar should cost money: %s", out[1].DeltaEndSavings)
	}
	// 450 USD/month less at 1.08 and 2%: 450×1.02/1.08 = 425.00 × 12 = 5100.00
	if got := out[2].DeltaEndSavings.String(); got != "5100.00" {
		t.Errorf("cut delta = %s", got)
	}

	// A scenario whose target disappears reports an error instead of failing the comparison.
	b2, _ := Apply(b, NewOp("remove_item", "tuition", nil))
	out, err = Compare(b2, nil, nil)
	if err != nil {
		t.Fatalf("stale scenario failed the comparison: %v", err)
	}
	if out[2].Error == "" {
		t.Errorf("stale scenario reports no error: %+v", out[2])
	}

	adopted, err := AdoptScenario(b, "cheaper")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "adopted tuition", itemByID(adopted, "tuition").Amount.String(), "450")
	eq(t, "scenarios left after adopt", len(adopted.Scenarios), 1)
	removed, err := RemoveScenario(b, "dollar-at-1-00")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	eq(t, "scenarios left after remove", len(removed.Scenarios), 1)
	if !reflect.DeepEqual(removed.Items, b.Items) {
		t.Error("removing a scenario changed the items")
	}
}

func TestBudgetJSONRoundTrip(t *testing.T) {
	b := fixture(t)
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	contains(t, "amounts must serialise as exact strings", string(raw), `"amount":"45.5"`)
	contains(t, "rates must serialise as exact strings", string(raw), `"per_base":"1.08"`)
	var back Budget
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	r1, r2 := mustReport(t, b), mustReport(t, back)
	if r1.Totals != r2.Totals {
		t.Fatal("round trip changed the numbers")
	}
}

func intp(i int) *int { return &i }

func TestScheduleByCalendarMonth(t *testing.T) {
	b := fixture(t)
	b.StartMonth = "2026-01"
	nb, err := Apply(b,
		NewOp("add_item", "", ItemInput{Name: "Insurance", Amount: money.MustDecimal("240"),
			Category: "home",
			Schedule: &ScheduleSpec{Starts: "2026-03", Ends: strp("2026-12"), Every: intp(3)}}),
		NewOp("add_item", "", ItemInput{Name: "Course", Amount: money.MustDecimal("90"),
			Category: "home",
			Schedule: &ScheduleSpec{Starts: "2026-05"}}), // every defaults to monthly in this form
		NewOp("add_item", "", ItemInput{Name: "Flights", Amount: money.MustDecimal("450"),
			Category: "home",
			Schedule: &ScheduleSpec{Starts: "2026-07", Every: intp(0)}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	insurance := itemByID(nb, "insurance").Schedule
	eq(t, "insurance start", insurance.Start, 2)
	eq(t, "insurance period", insurance.Every, 3)
	if insurance.End == nil {
		t.Error("insurance has no end")
	} else {
		eq(t, "insurance end", *insurance.End, 11)
	}
	if got := itemByID(nb, "course").Schedule; got != (Schedule{Start: 4, Every: 1}) {
		t.Errorf("course: %+v", got)
	}
	if got := itemByID(nb, "flights").Schedule; got != Once(6) {
		t.Errorf("flights: %+v", got)
	}
}

func TestSetItemSchedulePatchesByMonth(t *testing.T) {
	b := fixture(t)
	b.StartMonth = "2026-01"
	gym := ItemInput{Name: "Gym", Amount: money.MustDecimal("35"), Category: "home",
		Schedule: &ScheduleSpec{Starts: "2026-04", Every: intp(1)}}
	nb, err := Apply(b, NewOp("add_item", "", gym))
	if err != nil {
		t.Fatal(err)
	}
	// Only the end changes: start and period stay.
	setEnd := ItemPatch{Schedule: &ScheduleSpec{Ends: strp("2026-09")}}
	nb, err = Apply(nb, NewOp("set_item", "gym", setEnd))
	if err != nil {
		t.Fatal(err)
	}
	ended := itemByID(nb, "gym").Schedule
	eq(t, "start after setting the end", ended.Start, 3)
	eq(t, "period after setting the end", ended.Every, 1)
	if ended.End == nil {
		t.Fatal("setting the end left no end")
	}
	eq(t, "end", *ended.End, 8)
	// An empty ends removes the end.
	clearEnd := ItemPatch{Schedule: &ScheduleSpec{Ends: strp("")}}
	nb, err = Apply(nb, NewOp("set_item", "gym", clearEnd))
	if err != nil {
		t.Fatal(err)
	}
	if got := itemByID(nb, "gym").Schedule; got != (Schedule{Start: 3, Every: 1}) {
		t.Fatalf("after clearing ends: %+v", got)
	}
}

func TestScheduleSpecRejectsMixedAndEarlyMonths(t *testing.T) {
	b := fixture(t)
	b.StartMonth = "2026-01"
	bad := []*ScheduleSpec{
		{Starts: "2026-03", Start: intp(2)},      // both forms
		{Start: intp(1), Ends: strp("2026-06")},  // both forms
		{Starts: "2025-12"},                      // before the budget starts
		{Starts: "March"},                        // not YYYY-MM
		{Starts: "2026-02", Ends: strp("26-03")}, // not YYYY-MM
	}
	for _, s := range bad {
		item := ItemInput{Name: "X", Amount: money.MustDecimal("1"), Category: "home", Schedule: s}
		if _, err := Apply(b, NewOp("add_item", "", item)); err == nil {
			t.Errorf("accepted %+v", *s)
		}
	}
}

// A scenario stored with calendar months keeps meaning the same months after
// the budget's start month moves; the index form would silently shift.
func TestScenarioMonthsSurviveStartMonthChange(t *testing.T) {
	b := fixture(t)
	b.StartMonth = "2026-01"
	op := NewOp("add_item", "", ItemInput{Name: "Bonus", Amount: money.MustDecimal("500"),
		Category: "income", Schedule: &ScheduleSpec{Starts: "2026-06", Every: intp(0)}})
	b.StartMonth = "2026-03"
	nb, err := Apply(b, op)
	if err != nil {
		t.Fatal(err)
	}
	if got := itemByID(nb, "bonus").Schedule; got != Once(3) { // June is index 3 from March
		t.Fatalf("bonus: %+v", got)
	}
}

// add_item and set_item take a pot alongside a calendar-month schedule.
func TestItemPotWithMonthSchedule(t *testing.T) {
	b := fixture(t)
	b.StartMonth = "2026-01"
	nb, err := Apply(b, NewOp("add_item", "", map[string]any{
		"name":     "Bonus",
		"amount":   "500",
		"category": "income",
		"schedule": map[string]any{"starts": "2026-06", "every": 0},
		"pot":      "emergency",
	}))
	if err != nil {
		t.Fatal(err)
	}
	added := itemByID(nb, "bonus")
	eq(t, "added pot", added.Pot, "emergency")
	eq(t, "added schedule", added.Schedule, Schedule{Start: 5})
	move := ItemPatch{Schedule: &ScheduleSpec{Starts: "2026-07"}, Pot: strp("")}
	nb, err = Apply(nb, NewOp("set_item", "bonus", move))
	if err != nil {
		t.Fatal(err)
	}
	moved := itemByID(nb, "bonus")
	eq(t, "moved pot", moved.Pot, "")
	eq(t, "moved schedule", moved.Schedule, Schedule{Start: 6})
}
