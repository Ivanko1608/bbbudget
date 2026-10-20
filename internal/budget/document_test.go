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
	expectEqual(t, "pot balance", b.Pots[0].Balance.String(), "0.1")
	expectEqual(t, "rate", b.Currencies[0].PerBase.String(), "1.0800000001")
	expectEqual(t, "rent amount", b.Items[0].Amount.String(), "1234567890123.45")
	// Normalization: ids from names/labels, base currency, monthly default, kind, window.
	expectEqual(t, "category id", b.Categories[0].ID, "living")
	expectEqual(t, "category kind", b.Categories[0].Kind, KindExpense)
	expectEqual(t, "item id", b.Items[0].ID, "rent")
	expectEqual(t, "item currency", b.Items[0].Currency, "EUR")
	expectEqual(t, "item schedule", b.Items[0].Schedule, Monthly())
	expectEqual(t, "bill period", b.Items[1].Schedule.Every, 3)
	expectEqual(t, "months", b.Months, 12)

	expectEqual(t, "debts are empty", b.Debts == nil, false)
	expectEqual(t, "pots are empty", b.Pots == nil, false)
	expectEqual(t, "withdrawals are empty", b.Withdrawals == nil, false)
	expectEqual(t, "scenarios are empty", b.Scenarios == nil, false)
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
	if _, err := ParseDocument([]byte(strings.Repeat(" ", MaxDocumentBytes+1)), "json"); err == nil {
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
