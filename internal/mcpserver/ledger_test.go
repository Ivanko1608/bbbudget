package mcpserver

import (
	"strings"
	"testing"
)

func TestTransactionTools(t *testing.T) {
	h := setup(t) // today is 2026-10-08
	statement := []any{
		map[string]any{"date": "2026-10-02", "amount": "900", "category": "travel", "note": "Flights", "external_id": "row-1"},
		map[string]any{"date": "2026-10-03", "amount": "45.20", "category": "home", "external_id": "row-2"},
	}
	r := h.call("log_transactions", map[string]any{"transactions": statement}, false)
	if r["batch"] != "b1" || len(r["logged"].([]any)) != 2 {
		t.Fatalf("log: %v", r)
	}
	flags := r["flags"].(map[string]any)["2026-10"].([]any)
	if !strings.Contains(flags[0].(string), "Travel: €900 spent of €600 planned, €300 over.") {
		t.Errorf("flags: %v", flags)
	}
	// Pasting the same statement again logs nothing.
	r = h.call("log_transactions", map[string]any{"transactions": statement}, false)
	if len(r["logged"].([]any)) != 0 || len(r["skipped_already_logged"].([]any)) != 2 {
		t.Fatalf("re-import: %v", r)
	}
	st := h.call("month_status", map[string]any{}, false)
	if st["month"] != "2026-10" || st["spent"] != "945.20" || st["elapsed"].(float64) != 8 {
		t.Fatalf("status: %v", st)
	}
	if l := h.call("list_transactions", map[string]any{"category": "home"}, false)["items"].([]any); len(l) != 1 {
		t.Fatalf("list: %v", l)
	}
	if u := h.call("update_transaction", map[string]any{"id": "t2", "amount": "50"}, false); u["base"] != "50" {
		t.Fatalf("update: %v", u)
	}
	if d := h.call("delete_transactions", map[string]any{"batch": "b1"}, false); d["removed"].(float64) != 2 {
		t.Fatalf("delete: %v", d)
	}
	h.call("log_transactions", map[string]any{"transactions": []any{map[string]any{"date": "2026-10-02", "amount": "9", "category": "nope"}}}, true)
	h.call("month_status", map[string]any{"month": "1999-01"}, true)
}
