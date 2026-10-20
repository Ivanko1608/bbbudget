package mcpserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"bbbudget/internal/app"
	"bbbudget/internal/budget"
	"bbbudget/internal/money"
	"bbbudget/internal/rates"
	"bbbudget/internal/settings"
	"bbbudget/internal/store"
)

func eq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

type harness struct {
	t   *testing.T
	cs  *mcp.ClientSession
	app *app.App
}

func setup(t *testing.T) *harness {
	t.Helper()
	return setupWith(t, Options{DefaultBudget: "home", Version: "test"})
}

func setupWith(t *testing.T, opts Options) *harness {
	t.Helper()
	ctx := context.Background()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(st, nil)
	f := rates.Fixed{
		ID:    rates.CurrencyAPI,
		Base:  "EUR",
		Date:  "2026-10-07",
		Rates: map[string]string{"USD": "1.1186", "BTC": "0.00001"},
	}
	a.Rates = map[string]rates.Provider{
		rates.Auto:        f,
		rates.CurrencyAPI: f,
		rates.ECB:         f,
		rates.CoinGecko:   f,
	}
	a.Now = func() time.Time { return time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC) }
	a.SettingsPath = filepath.Join(t.TempDir(), "settings.json")
	on := true // fetching is off by default; these tests fetch
	if _, err := a.UpdateSettings(settings.Patch{FetchRates: &on}); err != nil {
		t.Fatal(err)
	}
	fromCLI := store.Origin{Source: store.SourceCLI}
	localizer := a.SettingsLocalizer()
	if _, err := a.Create(ctx, fromCLI, localizer, "home", "Home", "example"); err != nil {
		t.Fatal(err)
	}
	srv := New(a, opts)
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
	params := &mcp.CallToolParams{Name: name, Arguments: args}
	res, err := h.cs.CallTool(context.Background(), params)
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

func item(s map[string]any, id string) map[string]any {
	for _, l := range s["items"].([]any) {
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
	wantTools := []string{
		"list_budgets", "create_budget", "get_budget", "list_templates",
		"add_item", "update_item", "remove_item", "set_currency", "update_settings", "set_debt",
		"apply_ops", "what_if", "refresh_rates", "history", "undo",
		"save_scenario", "compare_scenarios", "adopt_scenario", "delete_scenario",
	}
	for _, want := range wantTools {
		if names[want] == nil {
			t.Errorf("missing tool %s", want)
		}
	}
	raw, _ := json.Marshal(names["add_item"].InputSchema)
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
	eq(t, "budget id", s["id"], "home")
	eq(t, "end savings", s["totals"].(map[string]any)["end_savings"], "10409.88")
	if item(s, "tuition")["base"] != "850.00" {
		t.Errorf("tuition base: %v", item(s, "tuition"))
	}
	if len(s["insights"].([]any)) == 0 {
		t.Error("no insights")
	}
}

func TestEditTools(t *testing.T) {
	h := setup(t)
	s := h.call("update_item", map[string]any{
		"detail": "full", "item": "school supplies", "amount": "60",
	}, false)
	if item(s, "supplies")["amount"] != "60.00" {
		t.Fatalf("update_item: %v", item(s, "supplies"))
	}
	s = h.call("add_item", map[string]any{
		"detail": "full", "name": "Insurance", "amount": "120", "category": "home",
		"every": 3, "starts": "2026-12",
	}, false)
	insurance := item(s, "insurance")
	if insurance == nil {
		t.Fatal("add_item: insurance missing")
	}
	eq(t, "insurance schedule", insurance["schedule"], "quarterly from Dec 2026")
	s = h.call("set_currency", map[string]any{
		"detail": "full", "code": "USD", "per_base": "1.2",
	}, false)
	if item(s, "supplies")["base"] != "51.00" { // 60 × 1.02 / 1.2
		t.Fatalf("set_currency: %v", item(s, "supplies"))
	}
	s = h.call("remove_item", map[string]any{"detail": "full", "item": "holiday"}, false)
	if item(s, "holiday") != nil {
		t.Fatal("remove_item")
	}
	// Ambiguous, unknown and malformed input are tool errors, not crashes.
	h.call("update_item", map[string]any{"item": "school", "amount": "1"}, true)
	h.call("update_item", map[string]any{"item": "rent", "amount": "1.001"}, true)
	h.call("update_item", map[string]any{"item": "rent", "amount": 800}, true) // number, not string
	h.call("get_budget", map[string]any{"budget": "nope"}, true)

	v, _ := h.app.Get(context.Background(), "home")
	if v.Revision != 5 {
		t.Errorf("expected 4 successful writes, revision is %d", v.Revision)
	}
}

func TestDebtUpsert(t *testing.T) {
	h := setup(t)
	s := h.call("set_debt", map[string]any{
		"detail": "full", "debt": "Credit cards", "currency": "USD", "balance": "4000",
		"apr_pct": "24", "min_payment": "250",
	}, false)
	debts := s["debts"].([]any)
	eq(t, "debts after create", len(debts), 1)
	eq(t, "created debt id", debts[0].(map[string]any)["id"], "credit-cards")
	s = h.call("set_debt", map[string]any{
		"detail": "full", "debt": "cards", "apr_pct": "25",
	}, false)
	debts = s["debts"].([]any)
	eq(t, "debts after update", len(debts), 1)
	eq(t, "updated debt apr", debts[0].(map[string]any)["apr_pct"], "25")
}

func TestDebtPayoff(t *testing.T) {
	h := setup(t)
	s := h.call("set_debt", map[string]any{
		"detail": "full", "debt": "Car loan", "balance": "2000", "apr_pct": "6",
		"min_payment": "100", "payoff_month": 2, "payoff_from": "General",
	}, false)
	var row map[string]any
	for _, d := range s["debts"].([]any) {
		if d.(map[string]any)["id"] == "car-loan" {
			row = d.(map[string]any)
		}
	}
	// The example fills the Emergency fund's target first, so General savings
	// is empty in Dec 2026: the payoff can't happen and the insight says so.
	if row == nil {
		t.Fatal("car loan missing after create")
	}
	eq(t, "payoff plan on create", row["payoff_plan"], "Dec 2026 from General savings")
	emptyPot := "Car loan: General savings holds nothing in Dec 2026"
	flagged := slices.ContainsFunc(s["insights"].([]any), func(in any) bool {
		return strings.Contains(in.(map[string]any)["text"].(string), emptyPot)
	})
	if !flagged {
		t.Errorf("an empty pot must be flagged: %v", s["insights"])
	}
	s = h.call("set_debt", map[string]any{
		"detail": "full", "debt": "car loan", "payoff_from": "",
	}, false)
	if s["months"].([]any)[2].(map[string]any)["payoff"] == nil {
		t.Errorf("a payoff from the month must show in month 2: %v", s["months"].([]any)[2])
	}
	s = h.call("set_debt", map[string]any{
		"detail": "full", "debt": "car loan", "payoff_month": -1,
	}, false)
	for _, d := range s["debts"].([]any) {
		if d.(map[string]any)["id"] == "car-loan" && d.(map[string]any)["payoff_plan"] != nil {
			t.Errorf("payoff not removed: %v", d)
		}
	}
}

func TestWhatIfDoesNotSave(t *testing.T) {
	h := setup(t)
	s := h.call("what_if", map[string]any{"detail": "full", "ops": []any{
		map[string]any{
			"op":     "set_currency",
			"target": "USD",
			"value":  map[string]any{"per_base": "1"},
		},
	}}, false)
	if !strings.Contains(s["id"].(string), "not saved") {
		t.Errorf("what-if not labelled: %v", s["id"])
	}
	after := h.call("get_budget", map[string]any{}, false)
	eq(t, "revision after what_if", after["revision"].(float64), 1)
	eq(t, "end savings after what_if", after["totals"].(map[string]any)["end_savings"], "10409.88")
}

func TestApplyOpsIsAtomic(t *testing.T) {
	h := setup(t)
	h.call("apply_ops", map[string]any{"ops": []any{
		map[string]any{"op": "set_item", "target": "rent", "value": map[string]any{"amount": "1"}},
		map[string]any{"op": "remove_item", "target": "ghost"},
	}}, true)
	s := h.call("get_budget", map[string]any{}, false)
	if item(s, "rent")["amount"] != "1150.00" {
		t.Fatal("partial apply")
	}
	h.call("apply_ops", map[string]any{"expected_revision": 99, "ops": []any{
		map[string]any{"op": "set_item", "target": "rent", "value": map[string]any{"amount": "1"}},
	}}, true)
	s = h.call("apply_ops", map[string]any{"detail": "full", "ops": []any{
		map[string]any{
			"op":    "add_goal",
			"value": map[string]any{"name": "Emergency", "target": "3000"},
		},
	}}, false)
	if len(s["goals"].([]any)) != 2 {
		t.Fatal("add_goal via apply_ops")
	}
}

func TestScenarioFlow(t *testing.T) {
	h := setup(t)
	h.call("save_scenario", map[string]any{"name": "Cut tuition", "ops": []any{
		map[string]any{
			"op":     "set_item",
			"target": "tuition",
			"value":  map[string]any{"amount": "450"},
		},
	}}, false)
	cmp := h.call("compare_scenarios", map[string]any{}, false)["items"].([]any)
	eq(t, "compared scenarios", len(cmp), 2)
	eq(t, "delta end savings", cmp[1].(map[string]any)["delta_end_savings_vs_baseline"], "5100.00")
	s := h.call("adopt_scenario", map[string]any{
		"detail": "full", "scenario": "cut-tuition",
	}, false)
	eq(t, "tuition after adopt", item(s, "tuition")["amount"], "450.00")
	if s["scenarios"] != nil {
		t.Fatalf("scenarios left after adopt: %v", s["scenarios"])
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
	home := &mcp.ReadResourceParams{URI: "budget://home"}
	res, err := h.cs.ReadResource(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Contents[0].Text, `"per_base": "1.08"`) {
		t.Fatalf("resource lacks the USD rate: %s", res.Contents[0].Text)
	}
}

func TestCryptoIncomeAndRates(t *testing.T) {
	h := setup(t)
	// A new currency without a rate gets one fetched.
	s := h.call("set_currency", map[string]any{
		"detail": "full", "code": "btc", "cash_out_fee_pct": "1",
	}, false)
	cur := s["settings"].(map[string]any)["currencies"].([]any)[2].(map[string]any)
	eq(t, "currency code", cur["code"], "BTC")
	eq(t, "currency per_base", cur["per_base"], "0.00001")
	eq(t, "currency source", cur["source"], "auto")
	eq(t, "currency cash_out_fee_pct", cur["cash_out_fee_pct"], "1")
	s = h.call("add_item", map[string]any{
		"detail": "full", "name": "Staking", "amount": "0.002", "currency": "BTC",
		"category": "income",
	}, false)
	if l := item(s, "staking"); l["base"] != "198.00" { // 0.002 BTC = €200, less 1%
		t.Fatalf("crypto income: %v", l)
	}
	h.call("add_item", map[string]any{
		"name": "Dust", "amount": "0.000000001", "currency": "BTC", "category": "income",
	}, true)

	r := h.call("refresh_rates", map[string]any{
		"detail": "full", "codes": []string{"USD"},
	}, false)
	u := r["rate_updates"].([]any)[0].(map[string]any)
	eq(t, "old rate", u["old"], "1.08")
	eq(t, "new rate", u["new"], "1.1186")
	eq(t, "rate provider", u["from"], "currency-api")
}

func TestRatesWhenOffline(t *testing.T) {
	h := setup(t)
	h.app.SettingsPath = filepath.Join(t.TempDir(), "settings.json")
	off := false
	if _, err := h.app.UpdateSettings(settings.Patch{FetchRates: &off}); err != nil {
		t.Fatal(err)
	}
	btcOffline := h.call("set_currency", map[string]any{"detail": "full", "code": "BTC"}, true)
	if e := btcOffline["error"].(string); !strings.Contains(e, "turned off") {
		t.Fatalf("set_currency: %s", e)
	}
	h.call("refresh_rates", map[string]any{"codes": []string{"USD"}}, true)
	// Nothing to fetch is not an error, and a typed rate still works offline.
	r := h.call("refresh_rates", map[string]any{"detail": "full"}, false)
	eq(t, "rate updates offline", len(r["rate_updates"].([]any)), 0)
	h.call("set_currency", map[string]any{"code": "BTC", "per_base": "0.00001"}, false)
}

func TestHistoryListsTheAssistantsChange(t *testing.T) {
	h := setup(t)
	h.call("update_item", map[string]any{"item": "rent", "amount": "1200"}, false)

	history := h.call("history", map[string]any{}, false)["items"].([]any)

	newest := history[0].(map[string]any)
	if newest["source"] != "mcp" {
		t.Errorf("source: %v", newest["source"])
	}
	if summary := newest["summary"].(string); !strings.Contains(summary, "1150 → 1200") {
		t.Errorf("summary: %q", summary)
	}
}

func TestUndoRevertsTheAssistantsChange(t *testing.T) {
	h := setup(t)
	h.call("update_item", map[string]any{"item": "rent", "amount": "1200"}, false)

	summary := h.call("undo", map[string]any{"detail": "full"}, false)

	if amount := item(summary, "rent")["amount"]; amount != "1150.00" {
		t.Errorf("rent after undo: %v", amount)
	}
}

// Someone else edits after the assistant: undoing the assistant's change is
// refused, and the error names the other change.
func TestUndoNamesChangesSomeoneElseMadeSince(t *testing.T) {
	h := setup(t)
	h.call("update_item", map[string]any{"item": "rent", "amount": "1300"}, false) // r2
	amount := money.MustDecimal("1350")
	webEdit := budget.NewOp("set_item", "rent", budget.ItemPatch{Amount: &amount})
	web := store.Origin{Source: store.SourceWeb}
	ops := []budget.Op{webEdit}
	ctx := context.Background()
	if _, err := h.app.Apply(ctx, web, "home", store.AnyRevision, ops); err != nil {
		t.Fatal(err)
	}

	undo := h.call("undo", map[string]any{"detail": "full", "revision": 2}, true)
	refusal := undo["error"].(string)

	if !strings.Contains(refusal, "r3 (web)") {
		t.Errorf("refusal doesn't name the web change: %s", refusal)
	}
}

// update_item with only an end date used to rebuild the schedule from
// "monthly from the first month", silently moving the item's start.
func TestUpdateItemEndKeepsStart(t *testing.T) {
	h := setup(t)
	h.call("add_item", map[string]any{
		"name": "Insurance", "amount": "120", "category": "home", "every": 3, "starts": "2026-12",
	}, false)
	s := h.call("update_item", map[string]any{
		"detail": "full", "item": "insurance", "ends": "2027-12",
	}, false)
	if l := item(s, "insurance"); l["schedule"] != "quarterly from Dec 2026 until Dec 2027" {
		t.Fatalf("ends only: %v", l)
	}
	s = h.call("update_item", map[string]any{
		"detail": "full", "item": "insurance", "ends": "",
	}, false)
	if l := item(s, "insurance"); l["schedule"] != "quarterly from Dec 2026" {
		t.Fatalf("clearing ends: %v", l)
	}
	s = h.call("update_item", map[string]any{
		"detail": "full", "item": "insurance", "every": 12,
	}, false)
	if l := item(s, "insurance"); l["schedule"] != "yearly from Dec 2026" {
		t.Fatalf("every only: %v", l)
	}
}

func TestApplyOpsTakesCalendarMonths(t *testing.T) {
	h := setup(t)
	s := h.call("apply_ops", map[string]any{"detail": "full", "ops": []any{
		map[string]any{"op": "add_item", "value": map[string]any{
			"name": "Bonus", "amount": "500", "category": "income",
			"schedule": map[string]any{"starts": "2027-03", "every": 0},
		}},
	}}, false)
	if l := item(s, "bonus"); l["schedule"] != "once, Mar 2027" {
		t.Fatalf("bonus: %v", l)
	}
	h.call("apply_ops", map[string]any{"ops": []any{
		map[string]any{"op": "add_item", "value": map[string]any{
			"name": "X", "amount": "1", "category": "home",
			"schedule": map[string]any{"starts": "2027-03", "start": 2},
		}},
	}}, true)
}

func TestInsightsAreStructured(t *testing.T) {
	h := setup(t)
	s := h.call("get_budget", map[string]any{}, false)
	in := s["insights"].([]any)[0].(map[string]any)
	for _, field := range []string{"key", "text", "tone"} {
		if in[field] == "" {
			t.Fatalf("insight has no %s: %v", field, in)
		}
	}
	if in["args"] == nil {
		t.Fatalf("insight has no args: %v", in)
	}
	if strings.Contains(in["text"].(string), "**") {
		t.Errorf("markdown left in text: %q", in["text"])
	}
}
