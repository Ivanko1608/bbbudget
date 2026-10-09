package assistant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"
)

var testTools = []Tool{{Name: "get_budget", Description: "the budget", Schema: map[string]any{
	"type": "object", "properties": map[string]any{"view": map[string]any{"type": "string"}}, "required": []any{"view"},
}}}

// A fake Messages API: the first reply asks for a tool, the second answers.
func TestAnthropicRequestsAndToolRoundTrip(t *testing.T) {
	var reqs []map[string]any
	var beta string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			t.Errorf("path %s", r.URL.Path)
		}
		beta = r.Header.Get("anthropic-beta")
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		reqs = append(reqs, body)
		w.Header().Set("Content-Type", "application/json")
		if len(reqs) == 1 {
			io.WriteString(w, `{"id":"m1","type":"message","role":"assistant","model":"claude-opus-5-5","stop_reason":"tool_use",
				"content":[{"type":"thinking","thinking":"","signature":"sig"},{"type":"text","text":"Checking."},{"type":"tool_use","id":"tu_1","name":"get_budget","input":{"view":"lines"}}],
				"usage":{"input_tokens":10,"output_tokens":5}}`)
			return
		}
		io.WriteString(w, `{"id":"m2","type":"message","role":"assistant","model":"claude-opus-5-5","stop_reason":"end_turn",
			"content":[{"type":"text","text":"Rent is your biggest line."}],"usage":{"input_tokens":10,"output_tokens":5}}`)
	}))
	defer srv.Close()

	p := NewAnthropic("test-key", "", option.WithBaseURL(srv.URL), option.WithMaxRetries(0))
	conv := p.Start("be brief", testTools)
	turn, err := conv.Send(context.Background(), "what's my biggest cost?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if turn.Text != "Checking." || len(turn.Calls) != 1 || turn.Calls[0].ID != "tu_1" || string(turn.Calls[0].Input) != `{"view":"lines"}` {
		t.Fatalf("turn 1: %+v", turn)
	}
	first := reqs[0]
	if first["model"] != DefaultAnthropicModel || first["fallbacks"] != "default" || first["cache_control"] == nil {
		t.Errorf("request: model %v fallbacks %v cache %v", first["model"], first["fallbacks"], first["cache_control"])
	}
	if oc, _ := first["output_config"].(map[string]any); oc["effort"] != "medium" {
		t.Errorf("effort: %v", first["output_config"])
	}
	if !strings.Contains(beta, "server-side-fallback-2026-07-01") {
		t.Errorf("beta header: %q", beta)
	}
	tool := first["tools"].([]any)[0].(map[string]any)
	if tool["name"] != "get_budget" || tool["input_schema"].(map[string]any)["required"].([]any)[0] != "view" {
		t.Errorf("tool: %v", tool)
	}

	turn, err = conv.Send(context.Background(), "", []ToolResult{{ID: "tu_1", Content: `{"lines":[]}`}})
	if err != nil || turn.Text != "Rent is your biggest line." || len(turn.Calls) != 0 {
		t.Fatalf("turn 2: %+v %v", turn, err)
	}
	msgs := reqs[1]["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("history: %d messages", len(msgs))
	}
	// The assistant turn goes back unchanged, thinking block first.
	asst := msgs[1].(map[string]any)["content"].([]any)
	if asst[0].(map[string]any)["type"] != "thinking" || asst[0].(map[string]any)["signature"] != "sig" {
		t.Errorf("thinking block not echoed: %v", asst[0])
	}
	res := msgs[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if res["type"] != "tool_result" || res["tool_use_id"] != "tu_1" {
		t.Errorf("tool result: %v", res)
	}
}

func TestAnthropicErrorsKeepTheHistoryValid(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			w.WriteHeader(401)
			io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		if n := len(body["messages"].([]any)); n != 1 {
			t.Errorf("a failed turn left %d messages in the history", n)
		}
		io.WriteString(w, `{"id":"m","type":"message","role":"assistant","model":"m","stop_reason":"end_turn","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer srv.Close()
	conv := NewAnthropic("bad", "claude-sonnet-5-5", option.WithBaseURL(srv.URL), option.WithMaxRetries(0)).Start("s", nil)
	if _, err := conv.Send(context.Background(), "hello", nil); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("401: %v", err)
	}
	if turn, err := conv.Send(context.Background(), "hello", nil); err != nil || turn.Text != "hi" {
		t.Fatalf("retry: %+v %v", turn, err)
	}
}

// A fake OpenAI-compatible server (what Ollama speaks).
func TestLocalToolRoundTrip(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("request: %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		bodies = append(bodies, body)
		if len(bodies) == 1 {
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"type":"function","function":{"name":"get_budget","arguments":"{\"view\":\"lines\"}"}}]}}]}`)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"Rent."}}]}`)
	}))
	defer srv.Close()
	p, err := NewLocal(srv.URL+"/v1", "llama3.1", "k")
	if err != nil {
		t.Fatal(err)
	}
	conv := p.Start("be brief", testTools)
	turn, err := conv.Send(context.Background(), "biggest cost?", nil)
	if err != nil || len(turn.Calls) != 1 || turn.Calls[0].ID == "" || string(turn.Calls[0].Input) != `{"view":"lines"}` {
		t.Fatalf("turn 1: %+v %v", turn, err)
	}
	turn, err = conv.Send(context.Background(), "", []ToolResult{{ID: turn.Calls[0].ID, Content: "{}"}})
	if err != nil || turn.Text != "Rent." {
		t.Fatalf("turn 2: %+v %v", turn, err)
	}
	msgs := bodies[1]["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	prev := msgs[len(msgs)-2].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	if last["role"] != "tool" || last["tool_call_id"] != prev["id"] {
		t.Errorf("tool result doesn't answer the call: %v vs %v", last, prev)
	}
	if bodies[0]["model"] != "llama3.1" || len(bodies[0]["tools"].([]any)) != 1 {
		t.Errorf("first request: %v", bodies[0])
	}
	if _, err := NewLocal("", "", ""); err == nil {
		t.Error("a model name is required")
	}
	if !isLoopback(DefaultLocalURL) || isLoopback("https://api.example.com/v1") {
		t.Error("isLoopback")
	}
}
