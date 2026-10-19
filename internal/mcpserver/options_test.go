package mcpserver

import (
	"context"
	"testing"

	"bbbudget/internal/store"
)

func TestWritesAreRecordedAsFromAnMCPClient(t *testing.T) {
	h := setup(t)

	h.call("update_item", map[string]any{"item": "rent", "amount": "1200"}, false)

	if source := h.lastChangeSource(); source != store.SourceMCP {
		t.Errorf("source: %s", source)
	}
}

func TestWritesAreRecordedWithTheConfiguredSource(t *testing.T) {
	h := setupWith(t, Options{DefaultBudget: "home", Source: store.SourceAssistant})

	h.call("update_item", map[string]any{"item": "rent", "amount": "1200"}, false)

	if source := h.lastChangeSource(); source != store.SourceAssistant {
		t.Errorf("source: %s", source)
	}
}

func (h *harness) lastChangeSource() store.Source {
	h.t.Helper()
	entries, err := h.app.History(context.Background(), "home", 1)
	if err != nil {
		h.t.Fatal(err)
	}
	return entries[0].Source
}

func TestOnlyToolsThatChangeNothingAreMarkedReadOnly(t *testing.T) {
	h := setup(t)
	tools, err := h.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	readOnly := map[string]bool{}
	for _, tool := range tools.Tools {
		readOnly[tool.Name] = tool.Annotations != nil && tool.Annotations.ReadOnlyHint
	}

	cases := map[string]bool{
		"get_budget":       true,
		"what_if":          true,
		"month_status":     true,
		"whats_new":        true,
		"update_item":      false,
		"apply_ops":        false,
		"log_transactions": false,
		"undo":             false,
	}
	for name, want := range cases {
		if readOnly[name] != want {
			t.Errorf("%s read-only: %v, want %v", name, readOnly[name], want)
		}
	}
}
