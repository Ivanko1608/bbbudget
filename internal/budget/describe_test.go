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
	nb, err := Apply(b,
		NewOp("set_item", "rent", ItemPatch{Amount: decp("1200"), Name: strp("Flat rent")}),
		NewOp("add_item", "", map[string]any{"name": "Gym", "amount": "35", "category": "home"}),
		NewOp("remove_item", "school supplies", nil),
		NewOp("set_settings", "", SettingsPatch{Months: intp(24)}),
	)
	if err != nil {
		t.Fatal(err)
	}
	got := DescribeChange(b, nb)
	for _, want := range []string{"months 12 → 24", "Rent: amount 1150 → 1200, name Rent → Flat rent", "added item Gym", "removed item School supplies"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestDescribeChangeIsCapped(t *testing.T) {
	b := fixture(t)
	var ops []Op
	for _, n := range []string{"A", "B", "C", "D", "E", "F", "G", "H"} {
		ops = append(ops, NewOp("add_item", "", map[string]any{"name": n, "amount": "1", "category": "home"}))
	}
	nb, err := Apply(b, ops...)
	if err != nil {
		t.Fatal(err)
	}
	if got := DescribeChange(b, nb); !strings.HasSuffix(got, "; and 2 more changes") || strings.Contains(got, "item H") {
		t.Errorf("capped: %q", got)
	}
}

func TestDescribeChangeCoversPots(t *testing.T) {
	b := fixture(t)
	nb, err := Apply(b,
		NewOp("add_pot", "", map[string]any{"name": "Holiday", "kind": "sinking", "balance": "0", "target": "900"}),
		NewOp("add_withdrawal", "", map[string]any{"name": "Car repair", "pot": "savings", "amount": "300"}),
	)
	if err != nil {
		t.Fatal(err)
	}
	got := DescribeChange(b, nb)
	for _, want := range []string{"added pot Holiday", "added withdrawal Car repair"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}
