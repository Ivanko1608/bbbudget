package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"bbbudget/internal/budget"
	"bbbudget/internal/money"
	"bbbudget/internal/store"
)

func TestPromptsAreOffered(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	res, err := h.cs.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, p := range res.Prompts {
		names[p.Name] = true
	}
	for _, want := range []string{"onboard", "monthly_review", "can_i_afford", "debt_plan", "import_statement"} {
		if !names[want] {
			t.Errorf("missing prompt %s", want)
		}
	}
	got, err := h.cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "can_i_afford", Arguments: map[string]string{"item": "a bike", "amount": "1200", "language": "Russian"}})
	if err != nil {
		t.Fatal(err)
	}
	text := got.Messages[0].Content.(*mcp.TextContent).Text
	for _, want := range []string{"a bike", "1200", "what_if", "Answer in Russian."} {
		if !strings.Contains(text, want) {
			t.Errorf("can_i_afford prompt lacks %q:\n%s", want, text)
		}
	}
	if _, err := h.cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "can_i_afford", Arguments: map[string]string{"item": "a bike"}}); err == nil {
		t.Error("a missing required argument should be an error")
	}
	// Every prompt names only tools that exist.
	tools, _ := h.cs.ListTools(ctx, nil)
	have := map[string]bool{}
	for _, tl := range tools.Tools {
		have[tl.Name] = true
	}
	for _, p := range prompts {
		text := p.text(map[string]string{"item": "x", "amount": "1"})
		for _, word := range strings.FieldsFunc(text, func(r rune) bool { return !(r == '_' || r >= 'a' && r <= 'z') }) {
			if strings.Contains(word, "_") && strings.Count(word, "_") <= 2 && looksLikeTool(word) && !have[word] {
				t.Errorf("prompt %s mentions %q, which is not a tool", p.name, word)
			}
		}
	}
}

// looksLikeTool matches the verb_noun shape of tool names, not fields like
// extra_to_debt_pct or external_id.
func looksLikeTool(w string) bool {
	for _, v := range []string{"get_", "list_", "create_", "add_", "update_", "remove_", "set_", "refresh_", "apply_", "what_", "save_", "compare_", "adopt_", "delete_", "log_", "month_", "whats_"} {
		if strings.HasPrefix(w, v) {
			return true
		}
	}
	return false
}

func TestWhatsNewShowsOthersChanges(t *testing.T) {
	h := setup(t)
	h.call("update_line", map[string]any{"line": "rent", "amount": "1200"}, false) // r2, by the assistant
	web := store.WithChange(context.Background(), store.Change{Source: "web"})
	amt := money.MustDecimal("700")
	if _, err := h.app.Apply(web, "home", store.AnyRevision, []budget.Op{budget.NewOp("set_line", "groceries", budget.LinePatch{Amount: &amt})}); err != nil {
		t.Fatal(err) // r3, by the person in the browser
	}
	w := h.call("whats_new", map[string]any{}, false)
	if w["since_revision"].(float64) != 2 || w["revision"].(float64) != 3 {
		t.Fatalf("revisions: %v", w)
	}
	others := w["changes_by_others"].([]any)
	if len(others) != 1 || others[0].(map[string]any)["by"] != "web" {
		t.Fatalf("others: %v", others)
	}
	if !strings.Contains(w["change"].(string), "amount 550 → 700") || effectOf(t, w)["totals"] == nil {
		t.Fatalf("effect: %v", w)
	}
	if w["this_month"] == nil {
		t.Error("this month's status missing")
	}
	// The same through the resource.
	res, err := h.cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "whats-new://home"})
	if err != nil || !strings.Contains(res.Contents[0].Text, `"changes_by_others"`) {
		t.Fatalf("resource: %v", err)
	}
	h.call("whats_new", map[string]any{"since_revision": 99}, true)
}

func TestWhatsNewBeforeTheAssistantEverWrote(t *testing.T) {
	h := setup(t)
	w := h.call("whats_new", map[string]any{}, false)
	if w["since_revision"].(float64) != 1 || len(w["changes_by_others"].([]any)) != 0 {
		t.Fatalf("fresh budget: %v", w)
	}
	if effectOf(t, w)["none"] == nil {
		t.Errorf("nothing changed yet: %v", w["effect"])
	}
}
