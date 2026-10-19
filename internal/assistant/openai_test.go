package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// modelServer is a fake OpenAI-compatible server: it answers with the
// replies in order and records the requests it got.
type modelServer struct {
	t              *testing.T
	replies        []string
	requests       []openAIRequest
	authorizations []string
}

func (s *modelServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var request openAIRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		s.t.Error(err)
	}
	s.requests = append(s.requests, request)
	s.authorizations = append(s.authorizations, r.Header.Get("Authorization"))
	reply := s.replies[0]
	s.replies = s.replies[1:]
	if reply == failure {
		http.Error(w, "overloaded", http.StatusServiceUnavailable)
		return
	}
	if _, err := io.WriteString(w, reply); err != nil {
		s.t.Error(err)
	}
}

const (
	failure       = "(fail)"
	callsGetItems = `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[` +
		`{"type":"function","function":{"name":"get_budget","arguments":"{\"view\":\"items\"}"}}` +
		`]}}]}`
	saysRent = `{"choices":[{"message":{"role":"assistant","content":"Rent."}}]}`
)

var getBudget = Tool{Name: "get_budget", Description: "The budget.", Schema: map[string]any{}}

func startOpenAI(t *testing.T, replies ...string) (Conversation, *modelServer) {
	t.Helper()
	server := &modelServer{t: t, replies: replies}
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	config := OpenAICompatibleConfig{BaseURL: httpServer.URL + "/v1/", Model: "llama3.1", APIKey: "k"}
	provider, err := NewOpenAICompatible(config)
	if err != nil {
		t.Fatal(err)
	}
	return provider.Start("be brief", []Tool{getBudget}), server
}

func TestOpenAICompatibleSendsTheModelAndTheTools(t *testing.T) {
	conversation, server := startOpenAI(t, saysRent)

	if _, err := conversation.SendText(context.Background(), "biggest cost?"); err != nil {
		t.Fatal(err)
	}

	request := server.requests[0]
	if request.Model != "llama3.1" {
		t.Errorf("model: %s", request.Model)
	}
	if len(request.Tools) != 1 {
		t.Errorf("tools: %v", request.Tools)
	}
}

func TestOpenAICompatibleSendsTheAPIKey(t *testing.T) {
	conversation, server := startOpenAI(t, saysRent)

	if _, err := conversation.SendText(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}

	if authorization := server.authorizations[0]; authorization != "Bearer k" {
		t.Errorf("authorization: %q", authorization)
	}
}

func TestOpenAICompatibleReturnsTheToolCalls(t *testing.T) {
	conversation, _ := startOpenAI(t, callsGetItems)

	turn, err := conversation.SendText(context.Background(), "biggest cost?")
	if err != nil {
		t.Fatal(err)
	}

	if len(turn.Calls) != 1 {
		t.Fatalf("calls: %v", turn.Calls)
	}
	if input := string(turn.Calls[0].Input); input != `{"view":"items"}` {
		t.Errorf("input: %s", input)
	}
	if turn.Calls[0].ID == "" {
		t.Error("a call the server left unnamed has no id")
	}
}

func TestOpenAICompatibleResultsAnswerTheirCall(t *testing.T) {
	conversation, server := startOpenAI(t, callsGetItems, saysRent)
	ctx := context.Background()
	turn, err := conversation.SendText(ctx, "biggest cost?")
	if err != nil {
		t.Fatal(err)
	}

	answer, err := conversation.SendResults(ctx, []ToolResult{{CallID: turn.Calls[0].ID, Content: "{}"}})
	if err != nil {
		t.Fatal(err)
	}

	if answer.Text != "Rent." {
		t.Errorf("answer: %q", answer.Text)
	}
	messages := server.requests[1].Messages
	result := messages[len(messages)-1]
	if result.ToolCallID != turn.Calls[0].ID {
		t.Errorf("the result answers %q, the call is %q", result.ToolCallID, turn.Calls[0].ID)
	}
}

func TestOpenAICompatibleForgetsAMessageTheServerFailed(t *testing.T) {
	conversation, server := startOpenAI(t, failure, saysRent)
	ctx := context.Background()
	if _, err := conversation.SendText(ctx, "hello"); err == nil {
		t.Fatal("a failed request was not an error")
	}

	if _, err := conversation.SendText(ctx, "hello"); err != nil {
		t.Fatal(err)
	}

	systemAndHello := 2
	if sent := len(server.requests[1].Messages); sent != systemAndHello {
		t.Errorf("the retry sent %d messages", sent)
	}
}

func TestOpenAICompatibleNeedsAModel(t *testing.T) {
	_, err := NewOpenAICompatible(OpenAICompatibleConfig{})

	if !errors.Is(err, ErrNoModel) {
		t.Errorf("error: %v", err)
	}
}
