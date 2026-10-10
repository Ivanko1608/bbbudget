package budget

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func mustApply(t *testing.T, b Budget, ops ...Op) Budget {
	t.Helper()
	changed, err := Apply(b, ops...)
	if err != nil {
		t.Fatal(err)
	}
	return changed
}

func TestDifferencesOfAnEditedBudget(t *testing.T) {
	before := fixture(t)
	after := mustApply(t, before,
		NewOp("set_item", "rent", ItemPatch{Amount: decp("1200"), Name: strp("Flat rent")}),
		NewOp("add_item", "", map[string]any{"name": "Gym", "amount": "35", "category": "home"}),
		NewOp("remove_item", "school supplies", nil),
		NewOp("set_settings", "", SettingsPatch{Months: intp(24)}),
	)

	got := Differences(before, after)

	want := []Difference{
		{Action: Changed, Part: PartSettings, Field: "months", Old: "12", New: "24"},
		{Action: Changed, Part: PartItem, Name: "Rent", Field: "name", Old: "Rent", New: "Flat rent"},
		{Action: Changed, Part: PartItem, Name: "Rent", Field: "amount", Old: "1150", New: "1200"},
		{Action: Added, Part: PartItem, Name: "Gym"},
		{Action: Removed, Part: PartItem, Name: "School supplies"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("differences:\n got %+v\nwant %+v", got, want)
	}
}

func TestDifferencesOfAnUnchangedBudget(t *testing.T) {
	b := fixture(t)

	got := Differences(b, b)

	if len(got) != 0 {
		t.Errorf("differences of a budget with itself: %+v", got)
	}
}

func TestDifferencesCoverPotsAndWithdrawals(t *testing.T) {
	before := fixture(t)
	after := mustApply(t, before,
		NewOp("add_pot", "", map[string]any{"name": "Holiday", "kind": "sinking", "balance": "0"}),
		NewOp("add_withdrawal", "", map[string]any{
			"name": "Car repair", "pot": "savings", "amount": "300",
		}),
	)

	got := Differences(before, after)

	want := []Difference{
		{Action: Added, Part: PartPot, Name: "Holiday"},
		{Action: Added, Part: PartWithdrawal, Name: "Car repair"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("differences:\n got %+v\nwant %+v", got, want)
	}
}

func TestDescribe(t *testing.T) {
	cases := []struct {
		name  string
		diffs []Difference
		want  string
	}{
		{"nothing", nil, "no changes"},
		{
			"fields of one thing share its name",
			[]Difference{
				{Action: Changed, Part: PartItem, Name: "Rent", Field: "name", Old: "Rent", New: "Flat rent"},
				{Action: Changed, Part: PartItem, Name: "Rent", Field: "amount", Old: "1150", New: "1200"},
			},
			"Rent: name Rent → Flat rent, amount 1150 → 1200",
		},
		{
			"settings have no name",
			[]Difference{{Action: Changed, Part: PartSettings, Field: "months", Old: "12", New: "24"}},
			"months 12 → 24",
		},
		{
			"added and removed",
			[]Difference{
				{Action: Added, Part: PartItem, Name: "Gym"},
				{Action: Removed, Part: PartPot, Name: "Car"},
			},
			"added item Gym; removed pot Car",
		},
		{
			"an unset value",
			[]Difference{{Action: Changed, Part: PartItem, Name: "Gym", Field: "pot", New: "savings"}},
			"Gym: pot (none) → savings",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Describe(c.diffs); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestDescribeIsCapped(t *testing.T) {
	var diffs []Difference
	for _, name := range strings.Split("A B C D E F G H", " ") {
		diffs = append(diffs, Difference{Action: Added, Part: PartItem, Name: name})
	}

	got := Describe(diffs)

	if !strings.HasSuffix(got, "; and 2 more changes") {
		t.Errorf("no count of what was left out: %q", got)
	}
	if strings.Contains(got, "item G") {
		t.Errorf("spells out more than %d things: %q", maxDescribed, got)
	}
}

// Every field of every part of a budget is compared, apart from the ones
// left out on purpose. A field added to a type must be added to its list.
func TestEveryFieldIsCompared(t *testing.T) {
	fetchDetails := []string{"rate_date", "rate_from"}
	cases := []struct {
		typ     reflect.Type
		fields  []field
		ignored []string
	}{
		{reflect.TypeFor[Budget](), settingsFields(Budget{}), listFields(reflect.TypeFor[Budget]())},
		{reflect.TypeFor[CurrencyRate](), currencyFields(CurrencyRate{}), fetchDetails},
		{reflect.TypeFor[Category](), categoryFields(Category{}), nil},
		{reflect.TypeFor[Item](), itemFields(Item{}), nil},
		{reflect.TypeFor[Debt](), debtFields(Debt{}), nil},
		{reflect.TypeFor[Pot](), potFields(Pot{}), nil},
		{reflect.TypeFor[Withdrawal](), withdrawalFields(Withdrawal{}), nil},
		{reflect.TypeFor[Scenario](), scenarioFields(Scenario{}), nil},
	}
	for _, c := range cases {
		t.Run(c.typ.Name(), func(t *testing.T) {
			compared := map[string]bool{}
			for _, f := range c.fields {
				compared[f.name] = true
			}
			for _, name := range jsonNames(c.typ) {
				if !compared[name] && !slices.Contains(c.ignored, name) {
					t.Errorf("%s.%s is never compared, so changes to it don't show in the history",
						c.typ.Name(), name)
				}
			}
		})
	}
}

func jsonNames(typ reflect.Type) []string {
	var names []string
	for i := range typ.NumField() {
		names = append(names, jsonName(typ.Field(i)))
	}
	return names
}

// listFields are a budget's lists, which have their own comparisons.
func listFields(typ reflect.Type) []string {
	var names []string
	for i := range typ.NumField() {
		if f := typ.Field(i); f.Type.Kind() == reflect.Slice {
			names = append(names, jsonName(f))
		}
	}
	return names
}

func jsonName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	return name
}
