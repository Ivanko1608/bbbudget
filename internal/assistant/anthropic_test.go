package assistant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// messagesServer is a fake Messages API: it answers with the replies in
// order and records the requests it got, decoded.
type messagesServer struct {
	t        *testing.T
	replies  []string
	requests []map[string]any
	betas    []string
}

func (s *messagesServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var request map[string]any
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		s.t.Error(err)
	}
	s.requests = append(s.requests, request)
	s.betas = append(s.betas, r.Header.Get("anthropic-beta"))
	reply := s.replies[0]
	s.replies = s.replies[1:]
	w.Header().Set("Content-Type", "application/json")
	if reply == rejectedKey {
		w.WriteHeader(http.StatusUnauthorized)
	}
	if _, err := io.WriteString(w, reply); err != nil {
		s.t.Error(err)
	}
}

const (
	rejectedKey = `{"type":"error",` +
		`"error":{"type":"authentication_error","message":"invalid x-api-key"}}`
	thinksAndCallsGetItems = `{"id":"m1","type":"message","role":"assistant",` +
		`"model":"claude-opus-5-5","stop_reason":"tool_use","content":[` +
		`{"type":"thinking","thinking":"","signature":"sig"},` +
		`{"type":"text","text":"Checking."},` +
		`{"type":"tool_use","id":"tu_1","name":"get_budget","input":{"view":"items"}}],` +
		`"usage":{"input_tokens":10,"output_tokens":5}}`
	answersRent = `{"id":"m2","type":"message","role":"assistant",` +
		`"model":"claude-opus-5-5","stop_reason":"end_turn",` +
		`"content":[{"type":"text","text":"Rent."}],` +
		`"usage":{"input_tokens":10,"output_tokens":5}}`
)

func stoppedFor(reason, text string) string {
	return `{"id":"m3","type":"message","role":"assistant",` +
		`"model":"claude-opus-5-5","stop_reason":"` + reason + `",` +
		`"content":[{"type":"text","text":"` + text + `"}],` +
		`"usage":{"input_tokens":10,"output_tokens":5}}`
}

var setItem = Tool{Name: "set_item", Description: "Set an item.", Schema: map[string]any{
	"type":                 "object",
	"properties":           map[string]any{"name": map[string]any{"type": "string"}},
	"required":             []any{"name"},
	"additionalProperties": false,
}}

func startAnthropic(t *testing.T, replies ...string) (Conversation, *messagesServer) {
	t.Helper()
	server := &messagesServer{t: t, replies: replies}
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	provider := NewAnthropic(
		AnthropicConfig{APIKey: "k"},
		option.WithBaseURL(httpServer.URL),
		option.WithMaxRetries(0),
	)
	return provider.Start(SystemPrompt{Instructions: "be brief", Context: "about Home"}, []Tool{setItem, getBudget}), server
}

func say(t *testing.T, conversation Conversation, text string) Turn {
	t.Helper()
	turn, err := conversation.SendText(context.Background(), text)
	if err != nil {
		t.Fatal(err)
	}
	return turn
}

func lastMessageContent(request map[string]any) map[string]any {
	messages := request["messages"].([]any)
	last := messages[len(messages)-1].(map[string]any)
	return last["content"].([]any)[0].(map[string]any)
}

func TestAnthropicRequestSettings(t *testing.T) {
	conversation, server := startAnthropic(t, answersRent)

	say(t, conversation, "biggest cost?")

	request := server.requests[0]
	effort := request["output_config"].(map[string]any)["effort"]
	cases := []struct {
		name string
		got  any
		want any
	}{
		{"model", request["model"], DefaultAnthropicModel},
		{"fallbacks", request["fallbacks"], "default"},
		{"effort", effort, "medium"},
		{"caching", request["cache_control"] != nil, true},
		{"beta", server.betas[0], string(anthropic.AnthropicBetaServerSideFallback2026_07_01)},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestAnthropicCachesInstructionsAndToolsButNotContext(t *testing.T) {
	conversation, server := startAnthropic(t, answersRent)

	say(t, conversation, "hi")

	request := server.requests[0]
	system := request["system"].([]any)
	instructions := system[0].(map[string]any)
	context := system[1].(map[string]any)
	tools := request["tools"].([]any)
	cases := []struct {
		name  string
		block map[string]any
		want  bool
	}{
		{"instructions", instructions, true},
		{"context", context, false},
		{"first tool", tools[0].(map[string]any), false},
		{"last tool", tools[len(tools)-1].(map[string]any), true},
	}
	for _, c := range cases {
		if got := c.block["cache_control"] != nil; got != c.want {
			t.Errorf("%s cached: got %v, want %v", c.name, got, c.want)
		}
	}
	if instructions["text"] != "be brief" || context["text"] != "about Home" {
		t.Errorf("system blocks: %v", system)
	}
}

func TestAnthropicToolsAreSortedByName(t *testing.T) {
	conversation, server := startAnthropic(t, answersRent)

	say(t, conversation, "hi")

	var names []string
	for _, tool := range server.requests[0]["tools"].([]any) {
		names = append(names, tool.(map[string]any)["name"].(string))
	}
	if want := []string{"get_budget", "set_item"}; !slices.Equal(names, want) {
		t.Errorf("tools: %v", names)
	}
}

func TestAnthropicToolSchemaKeepsItsRules(t *testing.T) {
	conversation, server := startAnthropic(t, answersRent)

	say(t, conversation, "hi")

	tools := server.requests[0]["tools"].([]any)
	schema := tools[1].(map[string]any)["input_schema"].(map[string]any)
	if required := schema["required"].([]any); len(required) != 1 || required[0] != "name" {
		t.Errorf("required: %v", required)
	}
	if schema["additionalProperties"] != false {
		t.Errorf("additionalProperties: %v", schema["additionalProperties"])
	}
	if schema["type"] != "object" {
		t.Errorf("type: %v", schema["type"])
	}
}

func TestAnthropicReturnsTheTextAndToolCalls(t *testing.T) {
	conversation, _ := startAnthropic(t, thinksAndCallsGetItems)

	turn := say(t, conversation, "biggest cost?")

	if turn.Text != "Checking." {
		t.Errorf("text: %q", turn.Text)
	}
	if len(turn.Calls) != 1 {
		t.Fatalf("calls: %v", turn.Calls)
	}
	if turn.Calls[0].ID != "tu_1" {
		t.Errorf("id: %s", turn.Calls[0].ID)
	}
	if input := string(turn.Calls[0].Input); input != `{"view":"items"}` {
		t.Errorf("input: %s", input)
	}
}

func TestAnthropicResultsAnswerTheirCall(t *testing.T) {
	conversation, server := startAnthropic(t, thinksAndCallsGetItems, answersRent)
	turn := say(t, conversation, "biggest cost?")

	results := []ToolResult{{CallID: turn.Calls[0].ID, Content: "{}"}}
	answer, err := conversation.SendResults(context.Background(), results)
	if err != nil {
		t.Fatal(err)
	}

	if answer.Text != "Rent." {
		t.Errorf("answer: %q", answer.Text)
	}
	result := lastMessageContent(server.requests[1])
	if result["tool_use_id"] != "tu_1" {
		t.Errorf("the result answers %v", result["tool_use_id"])
	}
}

func TestAnthropicSendsThinkingBackUnchanged(t *testing.T) {
	conversation, server := startAnthropic(t, thinksAndCallsGetItems, answersRent)
	turn := say(t, conversation, "biggest cost?")

	results := []ToolResult{{CallID: turn.Calls[0].ID, Content: "{}"}}
	if _, err := conversation.SendResults(context.Background(), results); err != nil {
		t.Fatal(err)
	}

	reply := server.requests[1]["messages"].([]any)[1].(map[string]any)
	thinking := reply["content"].([]any)[0].(map[string]any)
	if thinking["signature"] != "sig" {
		t.Errorf("thinking sent back: %v", thinking)
	}
}

func TestAnthropicRejectedCredentialsSayWhatToDo(t *testing.T) {
	conversation, _ := startAnthropic(t, rejectedKey)

	_, err := conversation.SendText(context.Background(), "hello")

	if err == nil || !strings.Contains(err.Error(), "check the API key") {
		t.Errorf("error: %v", err)
	}
}

func TestAnthropicForgetsAMessageTheAPIFailed(t *testing.T) {
	conversation, server := startAnthropic(t, rejectedKey, answersRent)
	if _, err := conversation.SendText(context.Background(), "hello"); err == nil {
		t.Fatal("a failed request was not an error")
	}

	say(t, conversation, "hello")

	if sent := len(server.requests[1]["messages"].([]any)); sent != 1 {
		t.Errorf("the retry sent %d messages", sent)
	}
}

func TestAnthropicStopReasons(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		want  string
	}{
		{"refusal without text", stoppedFor("refusal", ""), refusalText},
		{"refusal with text", stoppedFor("refusal", "No."), "No."},
		{"cut off", stoppedFor("max_tokens", "Rent is"), "Rent is" + truncatedNote},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			conversation, _ := startAnthropic(t, c.reply)

			turn := say(t, conversation, "hi")

			if turn.Text != c.want {
				t.Errorf("text: %q", turn.Text)
			}
		})
	}
}
