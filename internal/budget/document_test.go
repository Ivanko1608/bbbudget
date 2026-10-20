package budget

import (
	"strings"
	"testing"
)

func TestYAMLKeepsExactNumbers(t *testing.T) {
	b, err := ParseDocument([]byte(`
name: T
base_currency: EUR
start_month: "2026-10"
savings_start: 0.1
currencies: [{code: USD, per_base: 1.0800000001, transfer_fee_pct: 2}]
categories: [{label: Living}]
items:
  - {name: Rent, amount: 1234567890123.45, category: living}
  - {name: Bill, amount: "0.30", currency: USD, category: living, schedule: {every: 3}}
`), "yaml")
	if err != nil {
		t.Fatal(err)
	}
	// The old savings_start becomes the General savings pot, exactly.
	eq(t, "pot balance", b.Pots[0].Balance.String(), "0.1")
	eq(t, "rate", b.Currencies[0].PerBase.String(), "1.0800000001")
	eq(t, "rent amount", b.Items[0].Amount.String(), "1234567890123.45")
	// Normalization: ids from names/labels, base currency, monthly default, kind, window.
	eq(t, "category id", b.Categories[0].ID, "living")
	eq(t, "category kind", b.Categories[0].Kind, KindExpense)
	eq(t, "item id", b.Items[0].ID, "rent")
	eq(t, "item currency", b.Items[0].Currency, "EUR")
	eq(t, "item schedule", b.Items[0].Schedule, Monthly())
	eq(t, "bill period", b.Items[1].Schedule.Every, 3)
	eq(t, "months", b.Months, 12)

	eq(t, "debts is nil", b.Debts == nil, false)
	eq(t, "pots is nil", b.Pots == nil, false)
	eq(t, "withdrawals is nil", b.Withdrawals == nil, false)
	eq(t, "scenarios is nil", b.Scenarios == nil, false)
}

func TestDocumentRejectsSurprises(t *testing.T) {
	cases := map[string]string{
		"unknown field":   "name: T\nbase_curency: EUR\n",
		"unknown in item": "name: T\nitems: [{name: x, amout: 1}]\n",
		"exponent":        "name: T\nsavings_start: 1e3\n",
		"hex":             "name: T\nsavings_start: 0x10\n",
		"float months":    "name: T\nmonths: 1.5\n",
		"bad yaml":        "name: [unclosed\n",
		"non-string key":  "? [a]\n: 1\n",
	}
	for name, doc := range cases {
		if _, err := ParseDocument([]byte(doc), "yaml"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := ParseDocument([]byte(`{"name":"T","extra":1}`), "json"); err == nil {
		t.Error("json unknown field accepted")
	}
	if _, err := ParseDocument([]byte("{}"), "toml"); err == nil {
		t.Error("unknown format accepted")
	}
	oversized := strings.Repeat(" ", MaxDocumentBytes+1)
	if _, err := ParseDocument([]byte(oversized), "json"); err == nil {
		t.Error("oversized document accepted")
	}
	// Alias bombs are bounded rather than expanded without limit.
	bomb := "a: &a [x,x,x,x,x,x,x,x,x,x]\n" +
		"b: &b [*a,*a,*a,*a,*a,*a,*a,*a,*a,*a]\n" +
		"c: &c [*b,*b,*b,*b,*b,*b,*b,*b,*b,*b]\n" +
		"d: &d [*c,*c,*c,*c,*c,*c,*c,*c,*c,*c]\n" +
		"e: &e [*d,*d,*d,*d,*d,*d,*d,*d,*d,*d]\n" +
		"f: [*e,*e,*e,*e,*e,*e,*e,*e,*e,*e]\n"
	if _, err := ParseDocument([]byte(bomb), "yaml"); err == nil {
		t.Error("alias bomb accepted")
	}
}
