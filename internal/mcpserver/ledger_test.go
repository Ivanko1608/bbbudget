package mcpserver

import (
	"strings"
	"testing"
)

// statement is two rows of a bank statement, eight days into the budget's
// first month (setup's today).
var statement = []any{
	map[string]any{
		"date":        "2026-10-02",
		"amount":      "900",
		"category":    "travel",
		"note":        "Flights",
		"external_id": "row-1",
	},
	map[string]any{
		"date":        "2026-10-03",
		"amount":      "45.20",
		"category":    "home",
		"external_id": "row-2",
	},
}

func (h *harness) logStatement() map[string]any {
	h.t.Helper()
	return h.call("log_transactions", map[string]any{"transactions": statement}, false)
}

func TestLoggingAStatementReturnsTheBatch(t *testing.T) {
	h := setup(t)

	r := h.logStatement()

	if r["batch"] != "b1" {
		t.Errorf("batch: %v", r["batch"])
	}
	if logged := r["logged"].([]any); len(logged) != 2 {
		t.Errorf("logged: %v", logged)
	}
}

func TestLoggingReturnsTheMonthsFlags(t *testing.T) {
	h := setup(t)

	r := h.logStatement()

	october, _ := field(t, r, "flags", "2026-10").([]any)
	want := "Travel: €900 spent of €600 planned, €300 over."
	if len(october) == 0 || !strings.Contains(october[0].(string), want) {
		t.Errorf("flags: %v", r["flags"])
	}
}

func TestPastingTheSameStatementAgainLogsNothing(t *testing.T) {
	h := setup(t)
	h.logStatement()

	r := h.logStatement()

	if logged := r["logged"].([]any); len(logged) != 0 {
		t.Errorf("logged again: %v", logged)
	}
	if skipped, _ := r["skipped_already_logged"].([]any); len(skipped) != 2 {
		t.Errorf("skipped: %v", r["skipped_already_logged"])
	}
}

func TestMonthStatusTool(t *testing.T) {
	h := setup(t)
	h.logStatement()

	status := h.call("month_status", map[string]any{}, false)

	if status["month"] != "2026-10" {
		t.Errorf("month: %v", status["month"])
	}
	if status["spent"] != "945.20" {
		t.Errorf("spent: %v", status["spent"])
	}
	if status["elapsed"] != 8.0 {
		t.Errorf("elapsed: %v", status["elapsed"])
	}
}

func TestListTransactionsByCategory(t *testing.T) {
	h := setup(t)
	h.logStatement()

	home := h.call("list_transactions", map[string]any{"category": "home"}, false)

	if rows := home["items"].([]any); len(rows) != 1 {
		t.Errorf("home transactions: %v", rows)
	}
}

func TestUpdateTransactionValuesItAgain(t *testing.T) {
	h := setup(t)
	h.logStatement()

	updated := h.call("update_transaction", map[string]any{"id": "t2", "amount": "50"}, false)

	if updated["base"] != "50" {
		t.Errorf("base: %v", updated["base"])
	}
}

func TestDeleteTransactionsTakesBackABatch(t *testing.T) {
	h := setup(t)
	h.logStatement()

	deleted := h.call("delete_transactions", map[string]any{"batch": "b1"}, false)

	if deleted["removed"] != 2.0 {
		t.Errorf("removed: %v", deleted["removed"])
	}
}

func TestTransactionToolErrors(t *testing.T) {
	unknownCategory := []any{map[string]any{"date": "2026-10-02", "amount": "9", "category": "nope"}}
	cases := []struct {
		name string
		tool string
		args map[string]any
	}{
		{"unknown category", "log_transactions", map[string]any{"transactions": unknownCategory}},
		{"month outside the budget", "month_status", map[string]any{"month": "1999-01"}},
		{"neither ids nor batch", "delete_transactions", map[string]any{}},
		{"both ids and batch", "delete_transactions", map[string]any{"ids": []any{"t1"}, "batch": "b1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := setup(t)

			h.call(c.tool, c.args, true)
		})
	}
}
