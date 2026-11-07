package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"bbbudget/internal/app"
	"bbbudget/internal/assistant"
	"bbbudget/internal/settings"
)

// greeter is a model that says hello to everything.
type greeter struct{}

func (greeter) Name() settings.AssistantProvider { return "greeter" }
func (greeter) Model() string                    { return "test" }

func (greeter) Start(string, []assistant.Tool) assistant.Conversation { return greeting{} }

type greeting struct{}

func (greeting) SendText(context.Context, string) (assistant.Turn, error) {
	return assistant.Turn{Text: "Hello."}, nil
}

func (greeting) SendResults(context.Context, []assistant.ToolResult) (assistant.Turn, error) {
	return assistant.Turn{}, nil
}

func withGreeter(a *app.App) *assistant.Assistant {
	return assistant.New(
		a,
		func() (assistant.Provider, error) { return greeter{}, nil },
		slog.New(slog.DiscardHandler),
	)
}

// eventsOf decodes a server-sent event stream.
func eventsOf(t *testing.T, stream string) []assistant.Event {
	t.Helper()
	var events []assistant.Event
	for _, message := range strings.Split(strings.TrimSpace(stream), "\n\n") {
		var event assistant.Event
		data := strings.TrimPrefix(message, "data: ")
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			t.Fatalf("%v: %q", err, message)
		}
		events = append(events, event)
	}
	return events
}

func typesOf(events []assistant.Event) []assistant.EventType {
	var types []assistant.EventType
	for _, event := range events {
		types = append(types, event.Type)
	}
	return types
}

func TestChatStreamsTheReplyThenDone(t *testing.T) {
	c := setupWith(t, withGreeter)

	response := c.req("POST", "/api/budgets/home/chat", `{"message":"hi"}`, nil)

	if contentType := response.Header().Get("Content-Type"); contentType != "text/event-stream" {
		t.Errorf("content type: %s", contentType)
	}
	events := eventsOf(t, response.Body.String())
	want := []assistant.EventType{assistant.EventText, assistant.EventDone}
	if types := typesOf(events); !slices.Equal(types, want) {
		t.Fatalf("events: %v", types)
	}
	if events[0].Text != "Hello." {
		t.Errorf("text: %q", events[0].Text)
	}
	if events[1].Conversation == "" {
		t.Error("done names no conversation to continue")
	}
}

func TestAFailedReplyEndsWithAnErrorThenDone(t *testing.T) {
	c := setupWith(t, withGreeter)

	response := c.req("POST", "/api/budgets/nope/chat", `{"message":"hi"}`, nil)

	events := eventsOf(t, response.Body.String())
	want := []assistant.EventType{assistant.EventError, assistant.EventDone}
	if types := typesOf(events); !slices.Equal(types, want) {
		t.Fatalf("events: %v", types)
	}
	if events[0].Error == "" {
		t.Error("the error gives no reason")
	}
}

func TestChatWithoutAnAssistantIsNotFound(t *testing.T) {
	c := setup(t)

	c.do("POST", "/api/budgets/home/chat", `{"message":"hi"}`, 404)
}

func TestTheAssistantKeyStaysOnTheServer(t *testing.T) {
	c := setup(t)
	patch := `{"assistant_provider":"anthropic","assistant_api_key":"sk-secret"}`

	response := c.req("PUT", "/api/settings", patch, nil)

	if strings.Contains(response.Body.String(), "sk-secret") {
		t.Error("the settings response holds the key")
	}
	var view settingsView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.AssistantKeySet {
		t.Error("the settings don't say a key is set")
	}
	if !view.AssistantStatus.Ready {
		t.Errorf("status: %+v", view.AssistantStatus)
	}
}
