package budget

import (
	"strings"
	"testing"

	"bbbudget/internal/i18n"
)

// Rendered with the pseudo-locale, every piece of text in a report must be
// wrapped in ⟦…⟧: anything else bypassed the catalog and can't be translated.
func TestReportTextComesFromTheCatalog(t *testing.T) {
	b := fixture(t)
	b.Debts = append(b.Debts, Debt{ID: "loan", Name: "Loan", Currency: "EUR", Balance: d("900"), APRPct: d("12"), MinPayment: d("100")})
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
	}
	for _, m := range r.Months {
		wrapped("month label", m.Label)
	}
	for _, l := range r.Lines {
		wrapped("schedule of "+l.ID, l.ScheduleText)
	}
	for _, d := range r.Debts {
		if d.PaidOffMonth != nil {
			wrapped("paid-off label", d.PaidOffLabel)
		}
	}
	if len(r.Insights) < 8 {
		t.Fatalf("expected the fixture to produce most insights, got %d", len(r.Insights))
	}
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
		"будет достигнута в **дек. 2026**",
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
