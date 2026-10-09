package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (h *harness) raw(name string, args map[string]any) string {
	h.t.Helper()
	res, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil || res.IsError {
		h.t.Fatalf("%s: %v %v", name, err, res)
	}
	return res.Content[0].(*mcp.TextContent).Text
}

func effectOf(t *testing.T, r map[string]any) map[string]any {
	t.Helper()
	e, ok := r["effect"].(map[string]any)
	if !ok {
		t.Fatalf("no effect in %v", r)
	}
	return e
}

func TestWritesReturnWhatChanged(t *testing.T) {
	h := setup(t)
	r := h.call("update_line", map[string]any{"line": "rent", "amount": "1200"}, false)
	if r["saved"] != true || r["revision"].(float64) != 2 || r["currency"] != "EUR" {
		t.Fatalf("header: %v", r)
	}
	if c := r["change"].(string); !strings.Contains(c, "amount 1150 → 1200") {
		t.Errorf("change: %q", c)
	}
	e := effectOf(t, r)
	end := e["totals"].(map[string]any)["end_savings"].(map[string]any)
	if end["before"] != "10409.88" || !strings.HasPrefix(end["change"].(string), "-") {
		t.Errorf("end savings: %v", end)
	}
	if _, ok := e["first_month"].(map[string]any)["left"]; !ok {
		t.Errorf("first month left should move: %v", e["first_month"])
	}
	if e["updated_insights"] == nil {
		t.Error("raising the biggest line should update insights")
	}
	if r["lines"] != nil || r["months"] != nil {
		t.Error("a delta must not carry the whole budget")
	}
}

func TestDeltaShowsInsightsComingAndGoing(t *testing.T) {
	h := setup(t)
	r := h.call("add_line", map[string]any{"name": "New car", "amount": "9000", "category": "travel", "every": 0, "starts": "2027-03"}, false)
	if !strings.Contains(strings.Join(ids(effectOf(t, r)["new_insights"]), ","), "one_off_new-car") {
		t.Fatalf("new insight: %v", effectOf(t, r)["new_insights"])
	}
	r = h.call("remove_line", map[string]any{"line": "new car"}, false)
	if !strings.Contains(strings.Join(ids(effectOf(t, r)["gone_insights"]), ","), "one_off_new-car") {
		t.Fatalf("gone insight: %v", effectOf(t, r)["gone_insights"])
	}
}

func ids(v any) []string {
	var out []string
	list, _ := v.([]any)
	for _, x := range list {
		out = append(out, x.(map[string]any)["id"].(string))
	}
	return out
}

func TestChangeWithoutEffectSaysSo(t *testing.T) {
	h := setup(t)
	r := h.call("update_line", map[string]any{"line": "rent", "name": "Flat"}, false)
	e := effectOf(t, r)
	if e["totals"] != nil || !strings.Contains(r["change"].(string), "name Rent → Flat") {
		t.Fatalf("rename: %v", r)
	}
	// The biggest-line insight names the line, so it is the one thing that moved.
	if e["none"] != nil && e["updated_insights"] == nil {
		t.Fatalf("expected the renamed line's insight to update: %v", e)
	}
	r = h.call("update_settings", map[string]any{"name": "Our home"}, false)
	if effectOf(t, r)["none"] == nil {
		t.Fatalf("renaming the budget changes no outcome: %v", r["effect"])
	}
}

func TestWhatIfReturnsADelta(t *testing.T) {
	h := setup(t)
	r := h.call("what_if", map[string]any{"ops": []any{
		map[string]any{"op": "set_line", "target": "tuition", "value": map[string]any{"amount": "450"}},
	}}, false)
	if r["saved"] != false || r["revision"] != nil {
		t.Fatalf("what_if header: %v", r)
	}
	end := effectOf(t, r)["totals"].(map[string]any)["end_savings"].(map[string]any)
	if end["change"] != "5100.00" {
		t.Errorf("what_if effect: %v", end)
	}
	if v := h.call("get_budget", map[string]any{}, false); v["revision"].(float64) != 1 {
		t.Fatal("what_if saved")
	}
}

func TestGetBudgetViews(t *testing.T) {
	h := setup(t)
	o := h.call("get_budget", map[string]any{}, false)
	if n := len(o["months"].([]any)); n != overviewMonths+1 || o["months_note"] == nil {
		t.Fatalf("overview months: %d, note %v", n, o["months_note"])
	}
	if o["lines"] == nil || o["insights"] == nil || o["settings"] == nil {
		t.Fatal("overview should have everything else")
	}
	m := h.call("get_budget", map[string]any{"view": "months", "from": "2027-01", "to": "2027-03"}, false)
	if n := len(m["months"].([]any)); n != 3 || m["lines"] != nil || m["base_currency"] != "EUR" {
		t.Fatalf("months view: %v", m)
	}
	if a := h.call("get_budget", map[string]any{"view": "all"}, false); len(a["months"].([]any)) != 12 {
		t.Fatal("view all should keep every month")
	}
	if l := h.call("get_budget", map[string]any{"view": "lines"}, false); l["lines"] == nil || l["months"] != nil || l["insights"] != nil {
		t.Fatalf("lines view: %v", l)
	}
	h.call("get_budget", map[string]any{"view": "everything"}, true)
	h.call("get_budget", map[string]any{"view": "months", "from": "2030-01"}, true)
}

// The point of deltas: a write to a long budget costs a few hundred bytes of
// the model's context, not the whole projection again.
func TestWriteResponsesStaySmall(t *testing.T) {
	h := setup(t)
	h.call("update_settings", map[string]any{"months": 600}, false)
	small := h.raw("update_line", map[string]any{"line": "rent", "amount": "1200"})
	full := h.raw("update_line", map[string]any{"line": "rent", "amount": "1250", "detail": "full"})
	if len(small) > 3000 {
		t.Errorf("delta is %d bytes:\n%s", len(small), small)
	}
	if len(full) < 20*len(small) {
		t.Errorf("full %d bytes vs delta %d: expected the delta to be far smaller", len(full), len(small))
	}
	overview := h.raw("get_budget", map[string]any{})
	if len(overview) > 8000 {
		t.Errorf("overview of a 600-month budget is %d bytes", len(overview))
	}
	h.call("update_line", map[string]any{"line": "rent", "amount": "1"}, false)
	h.call("update_line", map[string]any{"line": "rent", "detail": "everything"}, true)
}
