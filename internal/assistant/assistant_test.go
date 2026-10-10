package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"bbbudget/internal/app"
	"bbbudget/internal/rates"
	"bbbudget/internal/store"
)

// script is a fake model: each Send returns the next turn and records what
// it was sent.
type script struct {
	turns   []Turn
	sent    []string
	results [][]ToolResult
	system  string
	tools   []Tool
	err     error
}

func (s *script) Name() string  { return "script" }
func (s *script) Model() string { return "fake" }
func (s *script) Start(system string, tools []Tool) Conversation {
	s.system, s.tools = system, tools
	return s
}
func (s *script) Send(_ context.Context, text string, results []ToolResult) (Turn, error) {
	if s.err != nil {
		return Turn{}, s.err
	}
	s.sent, s.results = append(s.sent, text), append(s.results, results)
	if len(s.turns) == 0 {
		return Turn{Text: "(script ran out)"}, nil
	}
	t := s.turns[0]
	s.turns = s.turns[1:]
	return t, nil
}

func call(id, name, input string) ToolCall {
	return ToolCall{ID: id, Name: name, Input: json.RawMessage(input)}
}

func newAssistant(t *testing.T, p Provider) (*Assistant, *app.App) {
	t.Helper()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(st, nil)
	a.Rates = map[string]rates.Provider{}
	a.Now = func() time.Time { return time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC) }
	if _, err := a.Create(context.Background(), "home", "Home", "example"); err != nil {
		t.Fatal(err)
	}
	return &Assistant{App: a, Provider: func() (Provider, error) { return p, nil }}, a
}

func TestReplyRunsToolsAndRecordsTheAssistant(t *testing.T) {
	p := &script{turns: []Turn{
		{Text: "Let me look.", Calls: []ToolCall{call("1", "get_budget", `{"view":"items"}`)}},
		{Calls: []ToolCall{call("2", "update_item", `{"item":"rent","amount":"1200"}`)}},
		{Text: "Done: rent is 1200 now. You can undo it."},
	}}
	as, a := newAssistant(t, p)
	var events []Event
	id, err := as.Reply(context.Background(), "", "home", "Raise rent to 1200", func(e Event) { events = append(events, e) })
	if err != nil || id == "" {
		t.Fatalf("reply: %q %v", id, err)
	}
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.Type)
	}
	if got := strings.Join(kinds, ","); got != "text,tool,tool,changed,text" {
		t.Fatalf("events: %s (%+v)", got, events)
	}
	if !strings.Contains(events[2].Detail, "amount 1150 → 1200") {
		t.Errorf("tool detail: %q", events[2].Detail)
	}
	// The model got the tool results back, the first one a real budget view.
	if len(p.results) != 3 || !strings.Contains(p.results[1][0].Content, `"items"`) || p.results[1][0].ID != "1" {
		t.Fatalf("results: %+v", p.results)
	}
	// The tools and instructions came from the MCP server.
	if len(p.tools) < 20 || !strings.Contains(p.system, `"home"`) || !strings.Contains(p.system, "what_if") {
		t.Errorf("tools %d, system %q", len(p.tools), p.system)
	}
	// The write is in the history as the assistant's.
	h, _ := a.History(context.Background(), "home", 1)
	if h[0].Source != Source {
		t.Fatalf("history source: %q", h[0].Source)
	}
	// A follow-up continues the same conversation.
	p.turns = []Turn{{Text: "Anything else?"}}
	id2, err := as.Reply(context.Background(), id, "home", "thanks", func(Event) {})
	if err != nil || id2 != id || p.sent[len(p.sent)-1] != "thanks" {
		t.Fatalf("follow-up: %q %v", id2, err)
	}
}

func TestReadsDontSignalChanges(t *testing.T) {
	p := &script{turns: []Turn{{Calls: []ToolCall{call("1", "month_status", `{}`)}}, {Text: "All good."}}}
	as, _ := newAssistant(t, p)
	var changed bool
	as.Reply(context.Background(), "", "home", "how is this month?", func(e Event) { changed = changed || e.Type == "changed" })
	if changed {
		t.Error("a read-only tool signalled a change")
	}
}

func TestToolErrorsGoBackToTheModel(t *testing.T) {
	p := &script{turns: []Turn{
		{Calls: []ToolCall{call("1", "update_item", `{"item":"nothing like it","amount":"1"}`), call("2", "get_budget", `not json`)}},
		{Text: "Sorry, which item?"},
	}}
	as, _ := newAssistant(t, p)
	if _, err := as.Reply(context.Background(), "", "home", "change it", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	r := p.results[1]
	if len(r) != 2 || !r[0].IsError || !r[1].IsError || !strings.Contains(r[1].Content, "not valid JSON") {
		t.Fatalf("results: %+v", r)
	}
}

func TestReplyGuards(t *testing.T) {
	loop := &script{}
	for range maxSteps + 2 {
		loop.turns = append(loop.turns, Turn{Calls: []ToolCall{call("x", "history", `{}`)}})
	}
	as, _ := newAssistant(t, loop)
	if _, err := as.Reply(context.Background(), "", "home", "go", func(Event) {}); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("runaway loop: %v", err)
	}
	if _, err := as.Reply(context.Background(), "", "home", "  ", func(Event) {}); !errors.Is(err, app.ErrBadInput) {
		t.Fatalf("empty message: %v", err)
	}
	if _, err := as.Reply(context.Background(), "", "nope", "hi", func(Event) {}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown budget: %v", err)
	}
	broken, _ := newAssistant(t, &script{err: errors.New("provider down")})
	if _, err := broken.Reply(context.Background(), "", "home", "hi", func(Event) {}); err == nil || !strings.Contains(err.Error(), "provider down") {
		t.Fatalf("provider error: %v", err)
	}
}

func TestIdleConversationsExpire(t *testing.T) {
	p := &script{turns: []Turn{{Text: "a"}, {Text: "b"}}}
	as, _ := newAssistant(t, p)
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	as.Now = func() time.Time { return now }
	id, _ := as.Reply(context.Background(), "", "home", "hi", func(Event) {})
	now = now.Add(2 * time.Hour)
	id2, _ := as.Reply(context.Background(), id, "home", "hi again", func(Event) {})
	if id2 == id {
		t.Fatal("an expired conversation was reused")
	}
}
