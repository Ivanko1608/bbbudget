package app

import (
	"context"
	"testing"

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
	v, err := a.Create(ctx, byTest, "home", "Дом", "example")
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

func historyIn(t *testing.T, a *App, locale string) []store.Entry {
	t.Helper()
	history, err := a.History(i18n.WithLocale(context.Background(), locale), "home", store.AllEntries)
	if err != nil {
		t.Fatal(err)
	}
	return history
}

// The history stores differences, so it reads in the reader's language
// whatever language the change was made in.
func TestHistoryReadsInTheReadersLanguage(t *testing.T) {
	a := testApp(t)
	mustSetRent(t, a, byBrowser, "1200")

	russian := historyIn(t, a, "ru")
	english := historyIn(t, a, "en")

	if got := russian[0].Summary; got != "Rent: сумма 1150 → 1200" {
		t.Errorf("Russian change: %q", got)
	}
	if got := russian[1].Summary; got != "создан" {
		t.Errorf("Russian creation: %q", got)
	}
	if got := english[0].Summary; got != "Rent: amount 1150 → 1200" {
		t.Errorf("English change: %q", got)
	}
}

func TestUndoReadsInTheReadersLanguage(t *testing.T) {
	a := testApp(t)
	mustSetRent(t, a, byBrowser, "1200")
	if _, err := a.Undo(context.Background(), byBrowser, "home", UndoRequest{}); err != nil {
		t.Fatal(err)
	}

	russian := historyIn(t, a, "ru")

	if got := russian[0].Summary; got != "отменено r2: Rent: сумма 1200 → 1150" {
		t.Errorf("Russian undo: %q", got)
	}
}
