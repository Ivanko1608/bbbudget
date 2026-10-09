package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
)

// DefaultAnthropicModel is used when the settings name no model.
const DefaultAnthropicModel = "claude-opus-5-5"

// Anthropic talks to Claude through the Messages API.
type Anthropic struct {
	client anthropic.Client
	model  string
}

// NewAnthropic returns a provider. An empty apiKey lets the SDK find
// credentials itself (ANTHROPIC_API_KEY, or a profile from "ant auth login").
// opts are extra client options (tests point the base URL at a fake server).
func NewAnthropic(apiKey, model string, opts ...option.RequestOption) *Anthropic {
	if model == "" {
		model = DefaultAnthropicModel
	}
	if apiKey != "" {
		opts = append([]option.RequestOption{option.WithAPIKey(apiKey)}, opts...)
	}
	return &Anthropic{client: anthropic.NewClient(opts...), model: model}
}

func (p *Anthropic) Name() string  { return "anthropic" }
func (p *Anthropic) Model() string { return p.model }

func (p *Anthropic) Start(system string, tools []Tool) Conversation {
	// Sorted, so the tool list (and with it the cached prompt prefix) is
	// byte-identical on every request.
	sorted := append([]Tool{}, tools...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	var ts []anthropic.BetaToolUnionParam
	for _, t := range sorted {
		schema := anthropic.BetaToolInputSchemaParam{Properties: t.Schema["properties"], ExtraFields: map[string]any{}}
		if req, ok := t.Schema["required"].([]any); ok {
			for _, r := range req {
				if s, ok := r.(string); ok {
					schema.Required = append(schema.Required, s)
				}
			}
		}
		if defs, ok := t.Schema["$defs"]; ok {
			schema.ExtraFields["$defs"] = defs
		}
		tool := anthropic.BetaToolParam{Name: t.Name, Description: anthropic.String(t.Description), InputSchema: schema}
		ts = append(ts, anthropic.BetaToolUnionParam{OfTool: &tool})
	}
	return &anthropicConv{p: p, system: system, tools: ts}
}

type anthropicConv struct {
	p        *Anthropic
	system   string
	tools    []anthropic.BetaToolUnionParam
	messages []anthropic.BetaMessageParam
}

func (c *anthropicConv) Send(ctx context.Context, text string, results []ToolResult) (Turn, error) {
	var blocks []anthropic.BetaContentBlockParamUnion
	for _, r := range results { // every result in one user message
		blocks = append(blocks, anthropic.NewBetaToolResultBlock(r.ID, r.Content, r.IsError))
	}
	if text != "" {
		blocks = append(blocks, anthropic.NewBetaTextBlock(text))
	}
	c.messages = append(c.messages, anthropic.NewBetaUserMessage(blocks...))

	resp, err := c.p.client.Beta.Messages.New(ctx, anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(c.p.model),
		MaxTokens: 16000,
		System:    []anthropic.BetaTextBlockParam{{Text: c.system}},
		Messages:  c.messages,
		Tools:     c.tools,
		// Thinking is on by default (adaptive). Medium effort is plenty for
		// budget questions and keeps replies quick.
		OutputConfig: anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortMedium},
		// Cache the system prompt and tools, which never change in a conversation.
		CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
		// If a safety classifier declines, let the API retry on a fallback
		// model instead of the chat just stopping.
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
		Fallbacks: anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()},
	})
	if err != nil {
		c.messages = c.messages[:len(c.messages)-1] // the turn didn't happen; keep the history valid
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			switch apiErr.StatusCode {
			case 401, 403:
				return Turn{}, fmt.Errorf("the Anthropic API rejected the credentials (HTTP %d); check the API key in Settings", apiErr.StatusCode)
			case 429:
				return Turn{}, errors.New("the Anthropic API is rate-limiting this key; try again in a moment")
			}
		}
		return Turn{}, fmt.Errorf("anthropic: %w", err)
	}
	// The whole response goes back into the history unchanged, thinking
	// blocks included: the API needs them on the next request.
	c.messages = append(c.messages, resp.ToParam())

	var turn Turn
	for _, block := range resp.Content {
		switch b := block.AsAny().(type) {
		case anthropic.BetaTextBlock:
			turn.Text += b.Text
		case anthropic.BetaToolUseBlock:
			turn.Calls = append(turn.Calls, ToolCall{ID: b.ID, Name: b.Name, Input: json.RawMessage(b.JSON.Input.Raw())})
		}
	}
	switch resp.StopReason {
	case anthropic.BetaStopReasonRefusal:
		if turn.Text == "" {
			turn.Text = "I can't help with that request."
		}
		turn.Calls = nil
	case anthropic.BetaStopReasonMaxTokens:
		// Tool calls stay: each needs a result in the next message. A cut-off
		// input fails to parse and goes back to the model as an error.
		if len(turn.Calls) == 0 {
			turn.Text += "\n\n(The answer was cut off.)"
		}
	}
	return turn, nil
}
