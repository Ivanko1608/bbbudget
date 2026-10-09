package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultLocalURL is Ollama's OpenAI-compatible endpoint.
const DefaultLocalURL = "http://127.0.0.1:11434/v1"

// Local talks to any server with an OpenAI-compatible chat completions API
// and tool calling: Ollama, LM Studio, llama.cpp's server, vLLM. Pointed at
// your own machine, nothing leaves it.
type Local struct {
	baseURL, model, apiKey string
	http                   *http.Client
}

// NewLocal returns a provider for baseURL (e.g. http://127.0.0.1:11434/v1).
func NewLocal(baseURL, model, apiKey string) (*Local, error) {
	if model == "" {
		return nil, errors.New("a model name is required for an OpenAI-compatible server (e.g. llama3.1 or qwen2.5)")
	}
	if baseURL == "" {
		baseURL = DefaultLocalURL
	}
	return &Local{baseURL: strings.TrimRight(baseURL, "/"), model: model, apiKey: apiKey, http: &http.Client{Timeout: 5 * time.Minute}}, nil
}

func (p *Local) Name() string  { return "openai-compatible" }
func (p *Local) Model() string { return p.model }

type oaMessage struct {
	Role       string       `json:"role"`
	Content    string       `json:"content"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type oaToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

func (p *Local) Start(system string, tools []Tool) Conversation {
	var ts []oaTool
	for _, t := range tools {
		var o oaTool
		o.Type = "function"
		o.Function.Name, o.Function.Description, o.Function.Parameters = t.Name, t.Description, t.Schema
		ts = append(ts, o)
	}
	return &localConv{p: p, tools: ts, messages: []oaMessage{{Role: "system", Content: system}}}
}

type localConv struct {
	p        *Local
	tools    []oaTool
	messages []oaMessage
}

func (c *localConv) Send(ctx context.Context, text string, results []ToolResult) (Turn, error) {
	n := len(c.messages)
	for _, r := range results {
		content := r.Content
		if r.IsError {
			content = "Error: " + content
		}
		c.messages = append(c.messages, oaMessage{Role: "tool", ToolCallID: r.ID, Content: content})
	}
	if text != "" {
		c.messages = append(c.messages, oaMessage{Role: "user", Content: text})
	}
	body, err := json.Marshal(map[string]any{"model": c.p.model, "messages": c.messages, "tools": c.tools})
	if err != nil {
		return Turn{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Turn{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.p.apiKey)
	}
	resp, err := c.p.http.Do(req)
	if err != nil {
		c.messages = c.messages[:n]
		return Turn{}, fmt.Errorf("can't reach the model server at %s (is it running?): %w", c.p.baseURL, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		c.messages = c.messages[:n]
		return Turn{}, fmt.Errorf("model server: HTTP %d: %s", resp.StatusCode, firstLine(string(raw)))
	}
	var out struct {
		Choices []struct {
			Message oaMessage `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		c.messages = c.messages[:n]
		return Turn{}, fmt.Errorf("model server: unexpected response: %s", firstLine(string(raw)))
	}
	m := out.Choices[0].Message
	m.Role = "assistant"
	c.messages = append(c.messages, m)
	turn := Turn{Text: m.Content}
	for i, tc := range m.ToolCalls {
		id := tc.ID
		if id == "" { // some servers leave it out
			id = fmt.Sprintf("call_%d_%d", n, i)
			c.messages[len(c.messages)-1].ToolCalls[i].ID = id
		}
		turn.Calls = append(turn.Calls, ToolCall{ID: id, Name: tc.Function.Name, Input: json.RawMessage(tc.Function.Arguments)})
	}
	return turn, nil
}

// isLoopback reports whether a base URL points at this machine.
func isLoopback(base string) bool {
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	h := u.Hostname()
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
