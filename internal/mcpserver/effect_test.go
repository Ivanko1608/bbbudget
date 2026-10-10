package mcpserver

import (
	"context"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// raw calls a tool and returns its text result, as the model receives it.
func (h *harness) raw(name string, args map[string]any) string {
	h.t.Helper()
	res, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		h.t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		h.t.Fatalf("%s: %v", name, res.Content)
	}
	return res.Content[0].(*mcp.TextContent).Text
}

func effectOf(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	e, ok := result["effect"].(map[string]any)
	if !ok {
		t.Fatalf("no effect in %v", result)
	}
	return e
}

func field(t *testing.T, m map[string]any, path ...string) any {
	t.Helper()
	var v any = m
	for _, key := range path {
		inner, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("no %v in %v", path, m)
		}
		v = inner[key]
	}
	return v
}

func insightIDs(t *testing.T, e map[string]any, which string) []string {
	t.Helper()
	list, _ := field(t, e, "insights", which).([]any)
	var ids []string
	for _, in := range list {
		ids = append(ids, in.(map[string]any)["id"].(string))
	}
	return ids
}

func TestAWriteReturnsTheChangeAndItsEffect(t *testing.T) {
	h := setup(t)

	r := h.call("update_item", map[string]any{"item": "rent", "amount": "1200"}, false)

	if r["saved"] != true {
		t.Errorf("saved: %v", r["saved"])
	}
	if r["revision"] != 2.0 {
		t.Errorf("revision: %v", r["revision"])
	}
	if r["change"] != "Rent: amount 1150 → 1200" {
		t.Errorf("change: %v", r["change"])
	}
	e := effectOf(t, r)
	if by, _ := field(t, e, "totals", "end_savings", "by").(string); by == "" || by[0] != '-' {
		t.Errorf("end savings should fall: %v", field(t, e, "totals", "end_savings"))
	}
	if field(t, e, "first_month", "left") == nil {
		t.Error("the first month's leftover should move")
	}
	if r["items"] != nil {
		t.Error("a change must not carry the whole budget")
	}
	if r["months"] != nil {
		t.Error("a change must not carry the projection")
	}
}

func TestAnInsightAppearsAndGoesAway(t *testing.T) {
	h := setup(t)
	newCar := map[string]any{
		"name":     "New car",
		"amount":   "9000",
		"category": "travel",
		"every":    0,
		"starts":   "2027-03",
	}

	added := effectOf(t, h.call("add_item", newCar, false))
	removed := effectOf(t, h.call("remove_item", map[string]any{"item": "new car"}, false))

	if !slices.Contains(insightIDs(t, added, "added"), "one_off_new-car") {
		t.Errorf("added insights: %v", field(t, added, "insights"))
	}
	if !slices.Contains(insightIDs(t, removed, "removed"), "one_off_new-car") {
		t.Errorf("removed insights: %v", field(t, removed, "insights"))
	}
}

func TestRenamingTheBudgetHasNoEffect(t *testing.T) {
	h := setup(t)

	r := h.call("update_settings", map[string]any{"name": "Our home"}, false)

	if effectOf(t, r)["none"] != true {
		t.Fatalf("renaming the budget changes no outcome: %v", r["effect"])
	}
}

func TestWhatIfReturnsTheEffectAndSavesNothing(t *testing.T) {
	h := setup(t)
	cheaperTuition := map[string]any{
		"op":     "set_item",
		"target": "tuition",
		"value":  map[string]any{"amount": "450"},
	}

	r := h.call("what_if", map[string]any{"ops": []any{cheaperTuition}}, false)

	if r["saved"] != false {
		t.Errorf("saved: %v", r["saved"])
	}
	if by := field(t, effectOf(t, r), "totals", "end_savings", "by"); by != "5100.00" {
		t.Errorf("end savings moved by %v", by)
	}
	if v := h.call("get_budget", map[string]any{}, false); v["revision"] != 1.0 {
		t.Errorf("what_if saved: revision %v", v["revision"])
	}
}

func TestAnUnknownDetailIsAnError(t *testing.T) {
	h := setup(t)

	h.call("update_item", map[string]any{"item": "rent", "detail": "everything"}, true)
}

func TestGetBudgetOverviewTrimsTheMonths(t *testing.T) {
	h := setup(t)

	o := h.call("get_budget", map[string]any{}, false)

	if n := len(o["months"].([]any)); n != overviewMonths+1 {
		t.Errorf("overview months: %d", n)
	}
	if o["months_note"] == nil {
		t.Error("the overview should say months were left out")
	}
	if o["items"] == nil {
		t.Error("the overview should have the items")
	}
	if o["insights"] == nil {
		t.Error("the overview should have the insights")
	}
}

func TestGetBudgetViews(t *testing.T) {
	cases := []struct {
		view   string
		has    []string
		hasNot []string
		months int
		// currency is where the view states the base currency.
		currency []string
	}{
		{
			view:     "all",
			has:      []string{"items", "settings", "insights"},
			months:   12,
			currency: []string{"settings", "base_currency"},
		},
		{view: "items", has: []string{"items", "categories"}, hasNot: []string{"months", "insights"}},
		{view: "months", has: []string{"months", "totals"}, hasNot: []string{"items"}, months: 12},
		{view: "goals", has: []string{"pots"}, hasNot: []string{"items", "months"}},
		{view: "insights", has: []string{"insights"}, hasNot: []string{"items", "months"}},
	}
	for _, c := range cases {
		t.Run(c.view, func(t *testing.T) {
			h := setup(t)

			v := h.call("get_budget", map[string]any{"view": c.view}, false)

			for _, key := range c.has {
				if v[key] == nil {
					t.Errorf("%s view should have %s", c.view, key)
				}
			}
			for _, key := range c.hasNot {
				if v[key] != nil {
					t.Errorf("%s view should not have %s", c.view, key)
				}
			}
			if months, _ := v["months"].([]any); c.months > 0 && len(months) != c.months {
				t.Errorf("%s view has %d months, want %d", c.view, len(months), c.months)
			}
			currency := c.currency
			if currency == nil {
				currency = []string{"base_currency"}
			}
			if code := field(t, v, currency...); code != "EUR" {
				t.Errorf("base currency: %v", code)
			}
		})
	}
}

func TestGetBudgetMonthRange(t *testing.T) {
	h := setup(t)
	firstQuarter := map[string]any{"view": "months", "from": "2027-01", "to": "2027-03"}

	v := h.call("get_budget", firstQuarter, false)

	if n := len(v["months"].([]any)); n != 3 {
		t.Errorf("months between 2027-01 and 2027-03: %d", n)
	}
}

func TestGetBudgetRejectsBadViewsAndEmptyRanges(t *testing.T) {
	h := setup(t)

	h.call("get_budget", map[string]any{"view": "everything"}, true)
	h.call("get_budget", map[string]any{"view": "months", "from": "2030-01"}, true)
}

// A write to a long budget must cost the model a few KB of context, not the
// whole projection again.
func TestWriteResponsesStaySmall(t *testing.T) {
	h := setup(t)
	h.call("update_settings", map[string]any{"months": 600}, false)

	change := h.raw("update_item", map[string]any{"item": "rent", "amount": "1200"})
	full := h.raw("update_item", map[string]any{"item": "rent", "amount": "1250", "detail": "full"})
	overview := h.raw("get_budget", map[string]any{})

	if len(change) > 3000 {
		t.Errorf("a change is %d bytes:\n%s", len(change), change)
	}
	if len(full) < 20*len(change) {
		t.Errorf("full is %d bytes, change %d: expected the change to be far smaller",
			len(full), len(change))
	}
	if len(overview) > 8000 {
		t.Errorf("the overview of a 600-month budget is %d bytes", len(overview))
	}
}
