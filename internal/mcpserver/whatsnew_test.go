package mcpserver

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"bbbudget/internal/budget"
	"bbbudget/internal/money"
	"bbbudget/internal/store"
)

// assistantThenBrowserEdit raises the rent as the assistant (r2), then the
// groceries in the browser (r3).
func (h *harness) assistantThenBrowserEdit() {
	h.t.Helper()
	h.call("update_item", map[string]any{"item": "rent", "amount": "1200"}, false)
	amount := money.MustDecimal("700")
	ops := []budget.Op{budget.NewOp("set_item", "groceries", budget.ItemPatch{Amount: &amount})}
	web := store.Origin{Source: store.SourceWeb}
	if _, err := h.app.Apply(context.Background(), web, "home", store.AnyRevision, ops); err != nil {
		h.t.Fatal(err)
	}
}

func TestWhatsNewStartsFromTheAssistantsLastChange(t *testing.T) {
	h := setup(t)
	h.assistantThenBrowserEdit()

	news := h.call("whats_new", map[string]any{}, false)

	if news["from_revision"] != 2.0 {
		t.Errorf("from_revision: %v", news["from_revision"])
	}
	if news["revision"] != 3.0 {
		t.Errorf("revision: %v", news["revision"])
	}
}

func TestWhatsNewListsWhatOthersChanged(t *testing.T) {
	h := setup(t)
	h.assistantThenBrowserEdit()

	news := h.call("whats_new", map[string]any{}, false)

	others := news["changes_by_others"].([]any)
	if len(others) != 1 {
		t.Fatalf("changes by others: %v", others)
	}
	if source := others[0].(map[string]any)["source"]; source != "web" {
		t.Errorf("source: %v", source)
	}
}

func TestWhatsNewLeavesOutTheAssistantsOwnChanges(t *testing.T) {
	h := setup(t)
	h.assistantThenBrowserEdit()

	news := h.call("whats_new", map[string]any{"from_revision": 1}, false)

	others := news["changes_by_others"].([]any)
	if len(others) != 1 {
		t.Errorf("changes by others from r1: %v", others)
	}
}

func TestWhatsNewSaysWhatTheChangesDid(t *testing.T) {
	h := setup(t)
	h.assistantThenBrowserEdit()

	news := h.call("whats_new", map[string]any{}, false)

	if change := news["change"].(string); !strings.Contains(change, "amount 550 → 700") {
		t.Errorf("change: %s", change)
	}
	if totals := effectOf(t, news)["totals"]; totals == nil {
		t.Errorf("no totals in the effect: %v", news["effect"])
	}
}

func TestWhatsNewIncludesThisMonth(t *testing.T) {
	h := setup(t)

	news := h.call("whats_new", map[string]any{}, false)

	if month := field(t, news, "this_month", "month"); month != "2026-10" {
		t.Errorf("this month: %v", month)
	}
}

func TestWhatsNewBeforeTheAssistantEverWrote(t *testing.T) {
	h := setup(t)

	news := h.call("whats_new", map[string]any{}, false)

	if news["from_revision"] != 1.0 {
		t.Errorf("from_revision: %v", news["from_revision"])
	}
	if others := news["changes_by_others"].([]any); len(others) != 0 {
		t.Errorf("changes by others: %v", others)
	}
	if none := effectOf(t, news)["none"]; none != true {
		t.Errorf("effect: %v", news["effect"])
	}
}

func TestWhatsNewRefusesAFutureRevision(t *testing.T) {
	h := setup(t)

	refusal := h.call("whats_new", map[string]any{"from_revision": 99}, true)

	if !strings.Contains(refusal["error"].(string), "r99 is newer than the budget") {
		t.Errorf("refusal: %v", refusal["error"])
	}
}

func TestWhatsNewResourceMatchesTheTool(t *testing.T) {
	h := setup(t)
	h.assistantThenBrowserEdit()
	fromTool := h.raw("whats_new", map[string]any{})

	params := &mcp.ReadResourceParams{URI: "whats-new://home"}
	resource, err := h.cs.ReadResource(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}

	if text := resource.Contents[0].Text; !jsonEqual(t, text, fromTool) {
		t.Errorf("resource:\n%s\ntool:\n%s", text, fromTool)
	}
}

func jsonEqual(t *testing.T, a, b string) bool {
	t.Helper()
	var decodedA, decodedB any
	if err := json.Unmarshal([]byte(a), &decodedA); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(b), &decodedB); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(decodedA, decodedB)
}

func TestWhatsNewLeavesOutAMonthOutsideTheBudget(t *testing.T) {
	h := setup(t)
	h.app.Now = func() time.Time { return time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC) }

	news := h.call("whats_new", map[string]any{}, false)

	if month, present := news["this_month"]; present {
		t.Errorf("this month: %v", month)
	}
}
