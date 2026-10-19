package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultOpenAICompatibleURL is Ollama's OpenAI-compatible endpoint.
const DefaultOpenAICompatibleURL = "http://127.0.0.1:11434/v1"

// OpenAICompatibleName names the provider in settings and status.
const OpenAICompatibleName = "openai-compatible"

// ErrNoModel means an OpenAI-compatible server was configured without a model.
var ErrNoModel = errors.New(
	"a model name is required for an OpenAI-compatible server (e.g. llama3.1 or qwen2.5)")

const (
	// modelTimeout allows for a slow local model on a long answer.
	modelTimeout = 5 * time.Minute
	// maxResponse bounds what is read from the model server.
	maxResponse = 8 << 20
)

type openAIRole string

const (
	roleSystem    openAIRole = "system"
	roleUser      openAIRole = "user"
	roleAssistant openAIRole = "assistant"
	roleTool      openAIRole = "tool"
)

// functionType is the only kind of tool and tool call the API has.
const functionType = "function"

// OpenAICompatibleConfig says where an OpenAI-compatible server is.
type OpenAICompatibleConfig struct {
	// BaseURL is the API root, e.g. http://127.0.0.1:11434/v1; default
	// DefaultOpenAICompatibleURL.
	BaseURL string
	Model   string
	// APIKey is sent as a bearer token when set.
	APIKey string
}

// OpenAICompatible talks to any server with an OpenAI-compatible chat
// completions API and tool calling: Ollama, LM Studio, llama.cpp's server,
// vLLM. Pointed at your own machine, nothing leaves it.
type OpenAICompatible struct {
	config OpenAICompatibleConfig
	http   *http.Client
}

// NewOpenAICompatible returns a provider for the server in config.
func NewOpenAICompatible(config OpenAICompatibleConfig) (*OpenAICompatible, error) {
	if config.Model == "" {
		return nil, ErrNoModel
	}
	if config.BaseURL == "" {
		config.BaseURL = DefaultOpenAICompatibleURL
	}
	config.BaseURL = strings.TrimRight(config.BaseURL, "/")
	return &OpenAICompatible{config: config, http: &http.Client{Timeout: modelTimeout}}, nil
}

func (p *OpenAICompatible) Name() string  { return OpenAICompatibleName }
func (p *OpenAICompatible) Model() string { return p.config.Model }

// BaseURL is the API root the provider talks to.
func (p *OpenAICompatible) BaseURL() string { return p.config.BaseURL }

type openAIMessage struct {
	Role       openAIRole       `json:"role"`
	Content    string           `json:"content"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openAIToolCall struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name string `json:"name"`
	// Arguments is a JSON object, encoded as a string.
	Arguments string `json:"arguments"`
}

type openAITool struct {
	Type     string             `json:"type"`
	Function openAIToolFunction `json:"function"`
}

type openAIToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

type openAIRequest struct {
	Model    string          `json:"model"`
	Messages []openAIMessage `json:"messages"`
	Tools    []openAITool    `json:"tools"`
}

type openAIResponse struct {
	Choices []openAIChoice `json:"choices"`
}

type openAIChoice struct {
	Message openAIMessage `json:"message"`
}

func (p *OpenAICompatible) Start(system string, tools []Tool) Conversation {
	offered := make([]openAITool, 0, len(tools))
	for _, tool := range tools {
		offered = append(offered, openAIToolOf(tool))
	}
	return &openAIConversation{
		provider: p,
		tools:    offered,
		messages: []openAIMessage{{Role: roleSystem, Content: system}},
	}
}

func openAIToolOf(tool Tool) openAITool {
	function := openAIToolFunction{
		Name:        tool.Name,
		Description: tool.Description,
		Parameters:  tool.Schema,
	}
	return openAITool{Type: functionType, Function: function}
}

type openAIConversation struct {
	provider *OpenAICompatible
	tools    []openAITool
	messages []openAIMessage
}

// Say forgets the message when the server fails, so the person can send it
// again.
func (c *openAIConversation) Say(ctx context.Context, text string) (Turn, error) {
	kept := len(c.messages)
	c.messages = append(c.messages, openAIMessage{Role: roleUser, Content: text})
	turn, err := c.complete(ctx)
	if err != nil {
		c.messages = c.messages[:kept]
	}
	return turn, err
}

// Answer keeps the results even when the server fails: the calls they
// answer have already happened.
func (c *openAIConversation) Answer(ctx context.Context, results []ToolResult) (Turn, error) {
	for _, result := range results {
		c.messages = append(c.messages, toolMessage(result))
	}
	return c.complete(ctx)
}

func toolMessage(result ToolResult) openAIMessage {
	content := result.Content
	if result.IsError {
		content = "Error: " + content
	}
	return openAIMessage{Role: roleTool, ToolCallID: result.CallID, Content: content}
}

func (c *openAIConversation) complete(ctx context.Context) (Turn, error) {
	request := openAIRequest{
		Model:    c.provider.config.Model,
		Messages: c.messages,
		Tools:    c.tools,
	}
	reply, err := c.provider.complete(ctx, request)
	if err != nil {
		return Turn{}, err
	}
	reply.Role = roleAssistant
	nameUnnamedCalls(reply.ToolCalls, len(c.messages))
	c.messages = append(c.messages, reply)
	return turnOf(reply), nil
}

// nameUnnamedCalls gives an id to each call the server left without one,
// as some do, so the results can answer it. The ids are unique within the
// conversation because position is the index of the reply in it.
func nameUnnamedCalls(calls []openAIToolCall, position int) {
	for i := range calls {
		if calls[i].ID == "" {
			calls[i].ID = fmt.Sprintf("call_%d_%d", position, i)
		}
	}
}

func turnOf(reply openAIMessage) Turn {
	turn := Turn{Text: reply.Content}
	for _, call := range reply.ToolCalls {
		turn.Calls = append(turn.Calls, ToolCall{
			ID:    call.ID,
			Name:  call.Function.Name,
			Input: json.RawMessage(call.Function.Arguments),
		})
	}
	return turn
}

func (p *OpenAICompatible) complete(
	ctx context.Context, request openAIRequest,
) (openAIMessage, error) {
	body, err := p.post(ctx, request)
	if err != nil {
		return openAIMessage{}, err
	}
	var response openAIResponse
	if err := json.Unmarshal(body, &response); err != nil || len(response.Choices) == 0 {
		return openAIMessage{}, fmt.Errorf("model server: unexpected response: %s",
			shorten(firstLine(string(body)), maxDetail))
	}
	return response.Choices[0].Message, nil
}

func (p *OpenAICompatible) post(ctx context.Context, request openAIRequest) ([]byte, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	endpoint := p.config.BaseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.config.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.config.APIKey)
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("can't reach the model server at %s (is it running?): %w",
			p.config.BaseURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model server: HTTP %d: %s",
			resp.StatusCode, shorten(firstLine(string(body)), maxDetail))
	}
	return body, nil
}
