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
