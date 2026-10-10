package budget

import (
	"slices"
	"strings"
	"testing"

	"bbbudget/internal/i18n"
)

// insightVariants are budgets that between them set off every insight rule.
func insightVariants(t *testing.T) []Budget {
	base := fixture(t)
	withDebt := fixture(t)
	withDebt.Debts = append(withDebt.Debts,
		Debt{ID: "loan", Name: "Loan", Currency: "USD", Balance: d("900"), APRPct: d("12"), MinPayment: d("100"), PayoffMonth: intp(3)},
		Debt{ID: "card", Name: "Card", Currency: "EUR", Balance: d("5000"), APRPct: d("30"), MinPayment: d("50"), PayoffMonth: intp(1), PayoffFrom: "spare"},
		Debt{ID: "car", Name: "Car loan", Currency: "EUR", Balance: d("9000"), APRPct: d("5"), MinPayment: d("10"), PayoffMonth: intp(0), PayoffFrom: "savings"})
	withDebt.Pots[0].Balance = d("1000")                                                                      // pays part of the car loan, then nothing is left for the card
	withDebt.Pots[1].Balance = d("2000")                                                                      // the emergency fund already holds its target
	withDebt.Pots = append(withDebt.Pots, Pot{ID: "spare", Name: "Spare", Kind: PotGeneral, Balance: d("0")}) // never receives anything
	short := fixture(t)
	short.Pots = []Pot{
		{ID: "savings", Name: "General savings", Kind: PotGeneral, Balance: d("300")},
		{ID: "car", Name: "New car", Kind: PotSinking, Balance: d("400"), Target: d("5000")},
		{ID: "rainy", Name: "Rainy day", Kind: PotEmergency, Balance: d("500")},
	}
	short.Items = append(short.Items, Item{ID: "roof", Name: "Roof", Amount: d("20000"), Currency: "EUR", Category: "home", Schedule: Schedule{Start: 1}})
	cut := fixture(t)
	cut.Withdrawals = []Withdrawal{{ID: "trip", Name: "Trip", Pot: "savings", Amount: d("5000"), Schedule: Schedule{Start: 0}}}
	potCost := fixture(t)
	potCost.Items = append(potCost.Items, Item{ID: "tv", Name: "TV", Amount: d("900"), Currency: "EUR", Category: "home", Schedule: Schedule{Start: 0}, Pot: "savings"})
	crypto := fixture(t)
	crypto.Currencies = append(crypto.Currencies, CurrencyRate{Code: "BTC", PerBase: d("0.00002"), TransferFeePct: d("0"), CashOutFeePct: d("1")})
	crypto.Items = append(crypto.Items,
		Item{ID: "btc", Name: "Bitcoin pay", Amount: d("0.05"), Currency: "BTC", Category: "income", Schedule: Monthly()},
		Item{ID: "bonus", Name: "Bonus", Amount: d("500"), Currency: "EUR", Category: "income", Schedule: Schedule{Start: 2}})
	cryptoShort := crypto
	cryptoShort.Items = append(slices.Clone(crypto.Items), Item{ID: "big-rent", Name: "Big rent", Amount: d("3000"), Currency: "EUR", Category: "home", Schedule: Monthly()})
	noEmergency := fixture(t)
	noEmergency.Pots = noEmergency.Pots[:1]
	return []Budget{base, withDebt, short, cut, potCost, crypto, cryptoShort, noEmergency}
}

// Rendered with the pseudo-locale, every piece of text in a report must be
// wrapped in ⟦…⟧: anything else bypassed the catalog and can't be
// translated. Between them the variants set off every rule.
func TestReportTextComesFromTheCatalog(t *testing.T) {
	fired := map[string]bool{}
	for _, b := range insightVariants(t) {
		r, err := Analyze(b)
		if err != nil {
			t.Fatal(err)
		}
		r.Localize(i18n.Pseudo())
		wrapped := func(what, s string) {
			if !strings.HasPrefix(s, "⟦") || !strings.HasSuffix(s, "⟧") {
				t.Errorf("%s isn't from the catalog: %q", what, s)
			}
		}
		for _, in := range r.Insights {
			wrapped("insight "+in.ID, in.Text)
			if strings.Contains(in.Text, "{") {
				t.Errorf("insight %s has an unfilled placeholder: %q", in.ID, in.Text)
			}
			fired[in.Key] = true
		}
		for _, m := range r.Months {
			wrapped("month label", m.Label)
		}
		for _, l := range r.Items {
			wrapped("schedule of "+l.ID, l.ScheduleText)
		}
		for _, d := range r.Debts {
			if d.PaidOffMonth != nil {
				wrapped("paid-off label", d.PaidOffLabel)
			}
		}
		for _, sf := range r.Shortfalls {
			wrapped("shortfall label", sf.Label)
		}
	}
	for _, k := range i18n.Catalogs()[i18n.Default].Keys() {
		if strings.HasPrefix(k, "insight.") && !insightParts[k] && !fired[k] {
			t.Errorf("no variant produces %s", k)
		}
	}
}

// insightParts are catalog messages that are pieces of an insight, not a
// whole one, so no insight has them as its key.
var insightParts = map[string]bool{
	"insight.category_share.item":     true,
	"insight.amount_in_base":          true,
	"insight.debt_payoff.month_money": true,
	"insight.shortfall.short":         true,
	"insight.shortfall.more":          true,
	"insight.shortfall.pot":           true,
	"insight.shortfall.sinking_pot":   true,
	"insight.shortfall.cover":         true,
	"insight.shortfall.last_resort":   true,
	"insight.shortfall.no_pots":       true,
	"insight.shortfall.owed":          true,
}

func TestReportInRussian(t *testing.T) {
	b := fixture(t)
	b.StartMonth = "2026-10"
	r, err := Analyze(b)
	if err != nil {
		t.Fatal(err)
	}
	r.Localize(i18n.For("ru"))
	if r.Months[0].Label != "окт. 2026" {
		t.Errorf("month label: %q", r.Months[0].Label)
	}
	var texts []string
	for _, in := range r.Insights {
		texts = append(texts, in.Text)
	}
	all := strings.Join(texts, "\n")
	for _, want := range []string{
		"Регулярные расходы в месяц: Home **1 853 €** (48%)",
		"Самая крупная регулярная статья: **Rent**, 1 223 € в месяц (31% дохода).",
		"дойдёт до 2\u00a0000\u00a0€ в **дек. 2026**",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	// Numbers are unchanged by translation; only text is.
	en, _ := Analyze(b)
	if en.Totals.EndSavings != r.Totals.EndSavings || len(en.Insights) != len(r.Insights) {
		t.Error("localizing changed the numbers")
	}
	for i := range en.Insights {
		if en.Insights[i].Key != r.Insights[i].Key {
			t.Errorf("insight %d key changed: %s vs %s", i, en.Insights[i].Key, r.Insights[i].Key)
		}
	}
}

func TestIssuesLocalize(t *testing.T) {
	b := fixture(t)
	b.Name = ""
	b.Months = 0
	_, err := Compile(b)
	ve, ok := err.(*ValidationError)
	if !ok || len(ve.Issues) != 2 {
		t.Fatalf("issues: %v", err)
	}
	if ve.Issues[0].Message != "is required" || ve.Issues[1].Message != "must be between 1 and 600" {
		t.Errorf("English: %+v", ve.Issues)
	}
	ru := ve.Issues[1].Localized(i18n.For("ru"))
	if ru.Message != "должно быть от 1 до 600" || ru.Path != "months" {
		t.Errorf("Russian: %+v", ru)
	}
}
