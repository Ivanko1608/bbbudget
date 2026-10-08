package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"bbbudget/internal/app"
	"bbbudget/internal/store"
)

type harness struct {
	t   *testing.T
	cs  *mcp.ClientSession
	app *app.App
}

func setup(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(st, nil)
	a.Now = func() time.Time { return time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC) }
	if _, err := a.Create(ctx, "home", "Home", "example"); err != nil {
		t.Fatal(err)
	}
	srv := New(a, Options{DefaultBudget: "home", Version: "test"})
	ct, srvT := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, srvT, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return &harness{t, cs, a}
}

// call invokes a tool and decodes its JSON text result. wantErr selects
// whether a tool-level error is expected.
func (h *harness) call(name string, args map[string]any, wantErr bool) map[string]any {
	h.t.Helper()
	res, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		h.t.Fatalf("%s: protocol error %v", name, err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if res.IsError != wantErr {
		h.t.Fatalf("%s: isError=%v, want %v: %s", name, res.IsError, wantErr, text)
	}
	if wantErr {
		return map[string]any{"error": text}
	}
	var out map[string]any
	if strings.HasPrefix(text, "[") {
		var arr []any
		json.Unmarshal([]byte(text), &arr)
		return map[string]any{"items": arr}
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		h.t.Fatalf("%s: %v\n%s", name, err, text)
	}
	return out
}

func line(s map[string]any, id string) map[string]any {
	for _, l := range s["lines"].([]any) {
		if m := l.(map[string]any); m["id"] == id {
			return m
		}
	}
	return nil
}

func TestToolsAreListedWithStringAmounts(t *testing.T) {
	h := setup(t)
	res, err := h.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]*mcp.Tool{}
	for _, tl := range res.Tools {
		names[tl.Name] = tl
	}
	for _, want := range []string{"list_budgets", "create_budget", "get_budget", "add_line", "update_line", "remove_line", "set_currency",
		"update_settings", "set_debt", "list_templates", "apply_ops", "what_if", "save_scenario", "compare_scenarios", "adopt_scenario", "delete_scenario"} {
		if names[want] == nil {
			t.Errorf("missing tool %s", want)
		}
	}
	raw, _ := json.Marshal(names["add_line"].InputSchema)
	var schema struct {
		Properties map[string]struct{ Type any } `json:"properties"`
	}
	json.Unmarshal(raw, &schema)
	if schema.Properties["amount"].Type != "string" {
		t.Errorf("amount must be a string in the schema: %s", raw)
	}
}

func TestGetBudget(t *testing.T) {
	h := setup(t)
	s := h.call("get_budget", map[string]any{}, false)
	if s["id"] != "home" || s["totals"].(map[string]any)["end_savings"] != "10409.88" {
		t.Fatalf("unexpected summary: %v", s["totals"])
	}
	if line(s, "tuition")["base"] != "850.00" {
		t.Errorf("tuition base: %v", line(s, "tuition"))
	}
	if len(s["insights"].([]any)) == 0 {
		t.Error("no insights")
	}
}

func TestEditTools(t *testing.T) {
	h := setup(t)
	s := h.call("update_line", map[string]any{"line": "school supplies", "amount": "60"}, false)
	if line(s, "supplies")["amount"] != "60.00" {
		t.Fatalf("update_line: %v", line(s, "supplies"))
	}
	s = h.call("add_line", map[string]any{"name": "Insurance", "amount": "120", "category": "home", "every": 3, "starts": "2026-12"}, false)
	if l := line(s, "insurance"); l == nil || l["schedule"] != "quarterly from Dec 2026" {
		t.Fatalf("add_line: %v", l)
	}
	s = h.call("set_currency", map[string]any{"code": "USD", "per_base": "1.2"}, false)
	if line(s, "supplies")["base"] != "51.00" { // 60 × 1.02 / 1.2
		t.Fatalf("set_currency: %v", line(s, "supplies"))
	}
	s = h.call("remove_line", map[string]any{"line": "holiday"}, false)
	if line(s, "holiday") != nil {
		t.Fatal("remove_line")
	}
	// Ambiguous, unknown and malformed input are tool errors, not crashes.
	h.call("update_line", map[string]any{"line": "school", "amount": "1"}, true)
	h.call("update_line", map[string]any{"line": "rent", "amount": "1.001"}, true)
	h.call("update_line", map[string]any{"line": "rent", "amount": 800}, true) // number, not string
	h.call("get_budget", map[string]any{"budget": "nope"}, true)

	v, _ := h.app.Get(context.Background(), "home")
	if v.Revision != 5 {
		t.Errorf("expected 4 successful writes, revision is %d", v.Revision)
	}
}

func TestDebtUpsert(t *testing.T) {
	h := setup(t)
	s := h.call("set_debt", map[string]any{"debt": "Credit cards", "currency": "USD", "balance": "4000", "apr_pct": "24", "min_payment": "250"}, false)
	debts := s["debts"].([]any)
	if len(debts) != 1 || debts[0].(map[string]any)["id"] != "credit-cards" {
		t.Fatalf("create: %v", debts)
	}
	s = h.call("set_debt", map[string]any{"debt": "cards", "apr_pct": "25"}, false)
	debts = s["debts"].([]any)
	if len(debts) != 1 || debts[0].(map[string]any)["apr_pct"] != "25" {
		t.Fatalf("update: %v", debts)
	}
}

func TestWhatIfDoesNotSave(t *testing.T) {
	h := setup(t)
	s := h.call("what_if", map[string]any{"ops": []any{
		map[string]any{"op": "set_currency", "target": "USD", "value": map[string]any{"per_base": "1"}},
	}}, false)
	if !strings.Contains(s["id"].(string), "not saved") {
		t.Errorf("what-if not labelled: %v", s["id"])
	}
	after := h.call("get_budget", map[string]any{}, false)
	if after["revision"].(float64) != 1 || after["totals"].(map[string]any)["end_savings"] != "10409.88" {
		t.Fatal("what_if changed the stored budget")
	}
}

func TestApplyOpsIsAtomic(t *testing.T) {
	h := setup(t)
	h.call("apply_ops", map[string]any{"ops": []any{
		map[string]any{"op": "set_line", "target": "rent", "value": map[string]any{"amount": "1"}},
		map[string]any{"op": "remove_line", "target": "ghost"},
	}}, true)
	s := h.call("get_budget", map[string]any{}, false)
	if line(s, "rent")["amount"] != "1150.00" {
		t.Fatal("partial apply")
	}
	h.call("apply_ops", map[string]any{"expected_revision": 99, "ops": []any{
		map[string]any{"op": "set_line", "target": "rent", "value": map[string]any{"amount": "1"}},
	}}, true)
	s = h.call("apply_ops", map[string]any{"ops": []any{
		map[string]any{"op": "add_goal", "value": map[string]any{"name": "Emergency", "target": "3000"}},
	}}, false)
	if len(s["goals"].([]any)) != 2 {
		t.Fatal("add_goal via apply_ops")
	}
}

func TestScenarioFlow(t *testing.T) {
	h := setup(t)
	h.call("save_scenario", map[string]any{"name": "Cut tuition", "ops": []any{
		map[string]any{"op": "set_line", "target": "tuition", "value": map[string]any{"amount": "450"}},
	}}, false)
	cmp := h.call("compare_scenarios", map[string]any{}, false)["items"].([]any)
	if len(cmp) != 2 || cmp[1].(map[string]any)["delta_end_savings_vs_baseline"] != "5100.00" {
		t.Fatalf("compare: %v", cmp)
	}
	s := h.call("adopt_scenario", map[string]any{"scenario": "cut-tuition"}, false)
	if line(s, "tuition")["amount"] != "450.00" || s["scenarios"] != nil {
		t.Fatalf("adopt: %v", s["scenarios"])
	}
	h.call("delete_scenario", map[string]any{"scenario": "cut-tuition"}, true)
}

func TestCreateAndResource(t *testing.T) {
	h := setup(t)
	h.call("create_budget", map[string]any{"id": "trip", "template": "blank"}, false)
	h.call("create_budget", map[string]any{"id": "../x"}, true)
	list := h.call("list_budgets", map[string]any{}, false)["items"].([]any)
	if len(list) != 2 {
		t.Fatalf("list: %v", list)
	}
	res, err := h.cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "budget://home"})
	if err != nil || !strings.Contains(res.Contents[0].Text, `"per_base": "1.08"`) {
		t.Fatalf("resource: %v", err)
	}
}
