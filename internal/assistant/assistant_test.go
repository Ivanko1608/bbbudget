package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"bbbudget/internal/app"
	"bbbudget/internal/rates"
	"bbbudget/internal/store"
)

// script is a fake model: each turn it returns the next scripted turn, and
// it records what it was sent.
type script struct {
	turns   []Turn
	said    []string
	answers [][]ToolResult
	system  string
	tools   []Tool
	fails   error
}

func (s *script) Name() string  { return "script" }
func (s *script) Model() string { return "fake" }

func (s *script) Start(system string, tools []Tool) Conversation {
	s.system = system
	s.tools = tools
	return s
}

func (s *script) Say(_ context.Context, text string) (Turn, error) {
	s.said = append(s.said, text)
	return s.next()
}

func (s *script) Answer(_ context.Context, results []ToolResult) (Turn, error) {
	s.answers = append(s.answers, results)
	return s.next()
}

func (s *script) next() (Turn, error) {
	if s.fails != nil {
		return Turn{}, s.fails
	}
	if len(s.turns) == 0 {
		return Turn{Text: "(the script ran out)"}, nil
	}
	turn := s.turns[0]
	s.turns = s.turns[1:]
	return turn, nil
}

func call(id, name, input string) ToolCall {
	return ToolCall{ID: id, Name: name, Input: json.RawMessage(input)}
}

func calls(toolCalls ...ToolCall) Turn { return Turn{Calls: toolCalls} }

func says(text string) Turn { return Turn{Text: text} }

var testNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func newAssistant(t *testing.T, model *script) *Assistant {
	t.Helper()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(st, nil)
	a.Rates = map[string]rates.Provider{}
	a.Now = func() time.Time { return testNow }
	fromCLI := store.Origin{Source: store.SourceCLI}
	_, err = a.Create(context.Background(), fromCLI, a.Localizer(""), "home", "Home", "example")
	if err != nil {
		t.Fatal(err)
	}
	assistant := New(a, func() (Provider, error) { return model, nil })
	assistant.Now = func() time.Time { return testNow }
	return assistant
}

// ask sends a message about the home budget and returns the events and the
// conversation id.
func ask(t *testing.T, assistant *Assistant, conversation, message string) ([]Event, string) {
	t.Helper()
	var events []Event
	collect := func(e Event) { events = append(events, e) }
	req := Request{Conversation: conversation, Budget: "home", Message: message}
	id, err := assistant.Reply(context.Background(), req, collect)
	if err != nil {
		t.Fatal(err)
	}
	return events, id
}

func typesOf(events []Event) []EventType {
	var types []EventType
	for _, e := range events {
		types = append(types, e.Type)
	}
	return types
}

func raiseTheRent() *script {
	return &script{turns: []Turn{
		{Text: "Let me look.", Calls: []ToolCall{call("1", "get_budget", `{"view":"items"}`)}},
		calls(call("2", "update_item", `{"item":"rent","amount":"1200"}`)),
		says("Done: rent is 1200 now. You can undo it."),
	}}
}

func TestAReplyRunsTheToolsTheModelAsksFor(t *testing.T) {
	assistant := newAssistant(t, raiseTheRent())

	events, _ := ask(t, assistant, "", "Raise rent to 1200")

	want := []EventType{EventText, EventTool, EventTool, EventChanged, EventText}
	if got := typesOf(events); !slices.Equal(got, want) {
		t.Errorf("events: %v, want %v", got, want)
	}
}

func TestAWriteSaysWhatItChanged(t *testing.T) {
	assistant := newAssistant(t, raiseTheRent())

	events, _ := ask(t, assistant, "", "Raise rent to 1200")

	if detail := events[2].Detail; !strings.Contains(detail, "amount 1150 → 1200") {
		t.Errorf("detail: %q", detail)
	}
}

func TestToolResultsGoBackToTheModel(t *testing.T) {
	model := raiseTheRent()
	assistant := newAssistant(t, model)

	ask(t, assistant, "", "Raise rent to 1200")

	first := model.answers[0][0]
	if first.CallID != "1" {
		t.Errorf("answers call %q", first.CallID)
	}
	if !strings.Contains(first.Content, `"items"`) {
		t.Errorf("content: %s", first.Content)
	}
}

func TestTheModelGetsTheServersTools(t *testing.T) {
	model := &script{}
	assistant := newAssistant(t, model)

	ask(t, assistant, "", "hi")

	hasWhatIf := func(tool Tool) bool { return tool.Name == "what_if" }
	if !slices.ContainsFunc(model.tools, hasWhatIf) {
		t.Errorf("tools: %v", model.tools)
	}
}

func TestTheModelIsToldWhichBudgetAndHowToUseTheTools(t *testing.T) {
	model := &script{}
	assistant := newAssistant(t, model)

	ask(t, assistant, "", "hi")

	if !strings.Contains(model.system, `"home"`) {
		t.Errorf("the system prompt doesn't name the budget:\n%s", model.system)
	}
	if !strings.Contains(model.system, "About the tools:") {
		t.Errorf("the system prompt lacks the server's instructions:\n%s", model.system)
	}
}

func TestTheAssistantsWritesAreRecordedAsItsOwn(t *testing.T) {
	assistant := newAssistant(t, raiseTheRent())

	ask(t, assistant, "", "Raise rent to 1200")

	entries, err := assistant.App.History(context.Background(), "home", 1)
	if err != nil {
		t.Fatal(err)
	}
	if source := entries[0].Source; source != store.SourceAssistant {
		t.Errorf("source: %s", source)
	}
}

func TestAFollowUpContinuesTheConversation(t *testing.T) {
	model := &script{}
	assistant := newAssistant(t, model)
	_, first := ask(t, assistant, "", "hi")

	_, second := ask(t, assistant, first, "thanks")

	if second != first {
		t.Errorf("conversation %q, then %q", first, second)
	}
}

func TestReadsDontSignalAChange(t *testing.T) {
	model := &script{turns: []Turn{calls(call("1", "month_status", `{}`)), says("All good.")}}
	assistant := newAssistant(t, model)

	events, _ := ask(t, assistant, "", "how is this month?")

	if slices.Contains(typesOf(events), EventChanged) {
		t.Errorf("a read signalled a change: %v", events)
	}
}

func TestToolErrorsGoBackToTheModel(t *testing.T) {
	cases := map[string]struct {
		call ToolCall
		want string
	}{
		"unknown item": {
			call: call("1", "update_item", `{"item":"nothing like it","amount":"1"}`),
			want: "nothing like it",
		},
		"input that isn't JSON": {
			call: call("1", "get_budget", `not json`),
			want: "not valid JSON",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			model := &script{turns: []Turn{calls(c.call), says("Sorry, which item?")}}
			assistant := newAssistant(t, model)

			ask(t, assistant, "", "change it")

			result := model.answers[0][0]
			if !result.IsError {
				t.Errorf("not an error: %s", result.Content)
			}
			if !strings.Contains(result.Content, c.want) {
				t.Errorf("content lacks %q: %s", c.want, result.Content)
			}
		})
	}
}

func TestAModelThatNeverStopsCallingToolsIsCutOff(t *testing.T) {
	model := &script{}
	for range MaxToolRounds + 2 {
		model.turns = append(model.turns, calls(call("1", "history", `{}`)))
	}
	assistant := newAssistant(t, model)
	req := Request{Budget: "home", Message: "go"}

	_, err := assistant.Reply(context.Background(), req, func(Event) {})

	if !errors.Is(err, ErrTooManyToolRounds) {
		t.Errorf("error: %v", err)
	}
}

func TestRefusedMessages(t *testing.T) {
	providerDown := errors.New("provider down")
	cases := map[string]struct {
		req   Request
		fails error
		want  error
	}{
		"empty message": {
			req:  Request{Budget: "home", Message: "  "},
			want: app.ErrBadInput,
		},
		"unknown budget": {
			req:  Request{Budget: "nope", Message: "hi"},
			want: store.ErrNotFound,
		},
		"provider failure": {
			req:   Request{Budget: "home", Message: "hi"},
			fails: providerDown,
			want:  providerDown,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			assistant := newAssistant(t, &script{fails: c.fails})

			_, err := assistant.Reply(context.Background(), c.req, func(Event) {})

			if !errors.Is(err, c.want) {
				t.Errorf("error: %v, want %v", err, c.want)
			}
		})
	}
}

func TestIdleConversationsAreForgotten(t *testing.T) {
	assistant := newAssistant(t, &script{})
	_, first := ask(t, assistant, "", "hi")
	later := testNow.Add(DefaultTTL + time.Minute)
	assistant.Now = func() time.Time { return later }

	_, second := ask(t, assistant, first, "hi again")

	if second == first {
		t.Error("an expired conversation was continued")
	}
}
