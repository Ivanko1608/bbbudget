package budget

import (
	"reflect"
	"strings"
	"testing"
)

func TestDescribe(t *testing.T) {
	b := fixture(t)
	if got := Describe(Differences(b, b)); got != "no changes" {
		t.Errorf("equal: %q", got)
	}
	nb, err := Apply(b,
		NewOp("set_item", "rent", ItemPatch{Amount: decp("1200"), Name: strp("Flat rent")}),
		NewOp("add_item", "", map[string]any{"name": "Gym", "amount": "35", "category": "home"}),
		NewOp("remove_item", "school supplies", nil),
		NewOp("set_settings", "", SettingsPatch{Months: intp(24)}),
	)
	if err != nil {
		t.Fatal(err)
	}
	got := Describe(Differences(b, nb))
	for _, want := range []string{"months 12 → 24", "Rent: amount 1150 → 1200, name Rent → Flat rent", "added item Gym", "removed item School supplies"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

// The history stores differences, not text, so each one must say exactly
// what changed.
func TestDifferencesAreStructured(t *testing.T) {
	b := fixture(t)
	nb, err := Apply(b,
		NewOp("set_item", "rent", ItemPatch{Amount: decp("1200"), Name: strp("Flat rent")}),
		NewOp("add_item", "", map[string]any{"name": "Gym", "amount": "35", "category": "home"}),
		NewOp("set_settings", "", SettingsPatch{Months: intp(24)}),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []Difference{
		{Kind: Changed, Field: "months", Old: "12", New: "24"},
		{Kind: Changed, Thing: "item", Name: "Rent", Field: "amount", Old: "1150", New: "1200"},
		{Kind: Changed, Thing: "item", Name: "Rent", Field: "name", Old: "Rent", New: "Flat rent"},
		{Kind: Added, Thing: "item", Name: "Gym"},
	}
	if got := Differences(b, nb); !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	unset := Describe([]Difference{{Kind: Changed, Thing: "item", Name: "Gym", Field: "pot", New: "savings"}})
	if unset != "Gym: pot (none) → savings" {
		t.Errorf("unset value: %q", unset)
	}
}

func TestDescribeIsCapped(t *testing.T) {
	b := fixture(t)
	var ops []Op
	for _, n := range []string{"A", "B", "C", "D", "E", "F", "G", "H"} {
		ops = append(ops, NewOp("add_item", "", map[string]any{"name": n, "amount": "1", "category": "home"}))
	}
	nb, err := Apply(b, ops...)
	if err != nil {
		t.Fatal(err)
	}
	if got := Describe(Differences(b, nb)); !strings.HasSuffix(got, "; and 2 more changes") || strings.Contains(got, "item H") {
		t.Errorf("capped: %q", got)
	}
}

func TestDifferencesCoverPots(t *testing.T) {
	b := fixture(t)
	nb, err := Apply(b,
		NewOp("add_pot", "", map[string]any{"name": "Holiday", "kind": "sinking", "balance": "0", "target": "900"}),
		NewOp("add_withdrawal", "", map[string]any{"name": "Car repair", "pot": "savings", "amount": "300"}),
	)
	if err != nil {
		t.Fatal(err)
	}
	got := Describe(Differences(b, nb))
	for _, want := range []string{"added pot Holiday", "added withdrawal Car repair"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

// Every field of Budget that isn't a list is a setting, and must be diffed.
func TestBudgetSettingsCoverEveryField(t *testing.T) {
	have := map[string]bool{}
	settings := reflect.TypeFor[budgetSettings]()
	for i := range settings.NumField() {
		have[settings.Field(i).Name] = true
	}
	doc := reflect.TypeFor[Budget]()
	for i := range doc.NumField() {
		if f := doc.Field(i); f.Type.Kind() != reflect.Slice && !have[f.Name] {
			t.Errorf("Budget.%s is missing from budgetSettings, so changes to it never show in the history", f.Name)
		}
	}
}
