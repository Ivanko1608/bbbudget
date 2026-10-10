package app

import (
	"context"
	"testing"

	"bbbudget/internal/budget"
	"bbbudget/internal/i18n"
	"bbbudget/internal/store"
)

// A budget made from a template in Russian gets the template's category and
// pot names in Russian; names the template doesn't have in the catalog stay.
func TestTemplateNamesFollowTheLocale(t *testing.T) {
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := New(st, nil)
	ctx := i18n.WithLocale(context.Background(), "ru")
	v, err := a.Create(ctx, "home", "Дом", "example")
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{}
	for _, c := range v.Budget.Categories {
		labels[c.ID] = c.Label
	}
	for _, p := range v.Budget.Pots {
		labels[p.ID] = p.Name
	}
	for id, want := range map[string]string{"income": "Доход", "home": "Жильё", "savings": "Общие сбережения", "emergency": "Резервный фонд"} {
		if labels[id] != want {
			t.Errorf("%s = %q, want %q", id, labels[id], want)
		}
	}
	if v.Report.Insights[0].Text == v.Report.Insights[0].Key {
		t.Errorf("insight not rendered: %+v", v.Report.Insights[0])
	}
}

// The history stores differences, so it reads in the reader's language
// whatever language the change was made in.
func TestHistoryReadsInTheReadersLanguage(t *testing.T) {
	a := testApp(t)
	if _, err := a.Apply(context.Background(), "home", store.AnyRevision, []budget.Op{rentOp("1200")}); err != nil {
		t.Fatal(err)
	}
	ru, err := a.History(i18n.WithLocale(context.Background(), "ru"), "home", 2)
	if err != nil {
		t.Fatal(err)
	}
	if ru[0].Summary != "Rent: сумма 1150 → 1200" || ru[1].Summary != "создан" {
		t.Errorf("Russian history: %q, %q", ru[0].Summary, ru[1].Summary)
	}
	en, _ := a.History(i18n.WithLocale(context.Background(), "en"), "home", 1)
	if en[0].Summary != "Rent: amount 1150 → 1200" {
		t.Errorf("English history: %q", en[0].Summary)
	}
}
