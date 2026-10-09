package budget

import (
	"encoding/json"
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
	if b.SavingsStart.String() != "0.1" || b.Currencies[0].PerBase.String() != "1.0800000001" || b.Items[0].Amount.String() != "1234567890123.45" {
		t.Fatalf("precision lost: %s %s %s", b.SavingsStart, b.Currencies[0].PerBase, b.Items[0].Amount)
	}
	// Normalization: ids from names/labels, base currency, monthly default, kind, window.
	if b.Categories[0].ID != "living" || b.Categories[0].Kind != KindExpense || b.Items[0].ID != "rent" ||
		b.Items[0].Currency != "EUR" || b.Items[0].Schedule != Monthly() || b.Items[1].Schedule.Every != 3 || b.Months != 12 {
		t.Fatalf("normalize: %+v %+v", b.Categories[0], b.Items)
	}
	if b.Debts == nil || b.Goals == nil || b.Scenarios == nil {
		t.Fatal("lists must be empty, not null")
	}
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
	bomb := "a: &a [x,x,x,x,x,x,x,x,x,x]\nb: &b [*a,*a,*a,*a,*a,*a,*a,*a,*a,*a]\nc: &c [*b,*b,*b,*b,*b,*b,*b,*b,*b,*b]\nd: &d [*c,*c,*c,*c,*c,*c,*c,*c,*c,*c]\ne: &e [*d,*d,*d,*d,*d,*d,*d,*d,*d,*d]\nf: [*e,*e,*e,*e,*e,*e,*e,*e,*e,*e]\n"
	if _, err := ParseDocument([]byte(bomb), "yaml"); err == nil {
		t.Error("alias bomb accepted")
	}
}

// Budgets saved before "lines" became "items" still load, in both formats,
// and save under the new name.
func TestOldLinesKeyStillLoads(t *testing.T) {
	for format, doc := range map[string]string{
		"yaml": "name: T\nbase_currency: EUR\ncategories: [{label: Living}]\nlines: [{name: Rent, amount: 950, category: living}]\n",
		"json": `{"name":"T","base_currency":"EUR","categories":[{"label":"Living"}],"lines":[{"name":"Rent","amount":"950","category":"living"}]}`,
	} {
		b, err := ParseDocument([]byte(doc), format)
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		if len(b.Items) != 1 || b.Items[0].ID != "rent" || b.Items[0].Amount.String() != "950" {
			t.Fatalf("%s: %+v", format, b.Items)
		}
		out, _ := json.Marshal(b)
		if !strings.Contains(string(out), `"items":[`) || strings.Contains(string(out), `"lines"`) {
			t.Fatalf("%s: saved as %s", format, out)
		}
	}
	both := "name: T\nbase_currency: EUR\nitems: []\nlines: []\n"
	if _, err := ParseDocument([]byte(both), "yaml"); err == nil || !strings.Contains(err.Error(), "lines") {
		t.Fatalf("items and lines together: %v", err)
	}
	if _, err := ParseDocument([]byte("name: T\nitemz: []\n"), "yaml"); err == nil {
		t.Fatal("unknown field accepted")
	}
}
