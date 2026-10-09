package budget

import (
	"strings"
	"testing"
)

func TestDescribeChange(t *testing.T) {
	b := fixture(t)
	if got := DescribeChange(b, b); got != "no changes" {
		t.Errorf("equal: %q", got)
	}
	months := 24 // not intp(): that helper arrives with another branch
	nb, err := Apply(b,
		NewOp("set_line", "rent", LinePatch{Amount: decp("1200"), Name: strp("Flat rent")}),
		NewOp("add_line", "", map[string]any{"name": "Gym", "amount": "35", "category": "home"}),
		NewOp("remove_line", "school supplies", nil),
		NewOp("set_settings", "", SettingsPatch{Months: &months}),
	)
	if err != nil {
		t.Fatal(err)
	}
	got := DescribeChange(b, nb)
	for _, want := range []string{"months 12 → 24", "Rent: amount 1150 → 1200, name Rent → Flat rent", "added line Gym", "removed line School supplies"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestDescribeChangeIsCapped(t *testing.T) {
	b := fixture(t)
	var ops []Op
	for _, n := range []string{"A", "B", "C", "D", "E", "F", "G", "H"} {
		ops = append(ops, NewOp("add_line", "", map[string]any{"name": n, "amount": "1", "category": "home"}))
	}
	nb, err := Apply(b, ops...)
	if err != nil {
		t.Fatal(err)
	}
	if got := DescribeChange(b, nb); !strings.HasSuffix(got, "; and 2 more changes") || strings.Contains(got, "line H") {
		t.Errorf("capped: %q", got)
	}
}
