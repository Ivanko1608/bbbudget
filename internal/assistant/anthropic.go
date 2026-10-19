package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"

	"bbbudget/internal/settings"
)

// DefaultAnthropicModel is used when the settings name no model.
const DefaultAnthropicModel = "claude-opus-5-5"

// maxAnswerTokens leaves room for thinking and a long answer without
// needing streaming.
const maxAnswerTokens = 16000

// cutOffNote ends an answer the token limit cut short.
const cutOffNote = "\n\n(The answer was cut off.)"

// refusalText stands in for an answer the model declined to give.
const refusalText = "I can't help with that request."

// AnthropicConfig says which Claude model to use, and with which key.
type AnthropicConfig struct {
	// APIKey may be empty: the SDK then finds credentials itself
	// (ANTHROPIC_API_KEY, or a profile from "ant auth login").
	APIKey string
	// Model defaults to DefaultAnthropicModel.
	Model string
}

// Anthropic talks to Claude through the Messages API.
type Anthropic struct {
	client anthropic.Client
	model  string
}

// NewAnthropic returns a provider. options are extra client options, such
// as another base URL.
func NewAnthropic(config AnthropicConfig, options ...option.RequestOption) *Anthropic {
	if config.APIKey != "" {
		options = append(options, option.WithAPIKey(config.APIKey))
	}
	model := cmp.Or(config.Model, DefaultAnthropicModel)
	return &Anthropic{client: anthropic.NewClient(options...), model: model}
}

func (p *Anthropic) Name() settings.AssistantProvider { return settings.AssistantAnthropic }
func (p *Anthropic) Model() string                    { return p.model }

func (p *Anthropic) Start(system string, tools []Tool) Conversation {
	// Sorted, so the tool list, and with it the cached prompt prefix, is the
	// same on every request.
	sorted := slices.SortedFunc(slices.Values(tools), func(a, b Tool) int {
		return cmp.Compare(a.Name, b.Name)
	})
	offered := make([]anthropic.BetaToolUnionParam, 0, len(sorted))
	for _, tool := range sorted {
		offered = append(offered, toAnthropicTool(tool))
	}
	return &anthropicConversation{provider: p, system: system, tools: offered}
}

func toAnthropicTool(tool Tool) anthropic.BetaToolUnionParam {
	param := anthropic.BetaToolParam{
		Name:        tool.Name,
		Description: anthropic.String(tool.Description),
		InputSchema: toInputSchema(tool.Schema),
	}
	return anthropic.BetaToolUnionParam{OfTool: &param}
}

// toInputSchema moves a decoded JSON Schema object into the SDK's type:
// properties and required have fields of their own, the rest goes along
// as it is.
func toInputSchema(schema any) anthropic.BetaToolInputSchemaParam {
	object, _ := schema.(map[string]any)
	param := anthropic.BetaToolInputSchemaParam{ExtraFields: map[string]any{}}
	for key, value := range object {
		switch key {
		case "type":
		case "properties":
			param.Properties = value
		case "required":
			param.Required = requiredNames(value)
		default:
			param.ExtraFields[key] = value
		}
	}
	return param
}

func requiredNames(value any) []string {
	listed, _ := value.([]any)
	var names []string
	for _, entry := range listed {
		if name, ok := entry.(string); ok {
			names = append(names, name)
		}
	}
	return names
}

type anthropicConversation struct {
	provider *Anthropic
	system   string
	tools    []anthropic.BetaToolUnionParam
	messages []anthropic.BetaMessageParam
}

// Say forgets the message when the request fails, so the person can send it
// again.
func (c *anthropicConversation) Say(ctx context.Context, text string) (Turn, error) {
	kept := len(c.messages)
	message := anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(text))
	c.messages = append(c.messages, message)
	turn, err := c.complete(ctx)
	if err != nil {
		c.messages = c.messages[:kept]
	}
	return turn, err
}

// Answer keeps the results even when the request fails: the calls they
// answer have already happened, and the API joins them with the person's
// next message.
func (c *anthropicConversation) Answer(ctx context.Context, results []ToolResult) (Turn, error) {
	blocks := make([]anthropic.BetaContentBlockParamUnion, 0, len(results))
	for _, result := range results {
		block := anthropic.NewBetaToolResultBlock(result.CallID, result.Content, result.IsError)
		blocks = append(blocks, block)
	}
	// Every result in one message, so the model keeps calling tools in
	// parallel.
	c.messages = append(c.messages, anthropic.NewBetaUserMessage(blocks...))
	return c.complete(ctx)
}

func (c *anthropicConversation) complete(ctx context.Context) (Turn, error) {
	response, err := c.provider.client.Beta.Messages.New(ctx, c.request())
	if err != nil {
		return Turn{}, explainAnthropicError(err)
	}
	// The response goes back into the history unchanged, thinking blocks
	// included: the API needs them on the next request.
	c.messages = append(c.messages, response.ToParam())
	return turnFromAnthropic(response), nil
}

func (c *anthropicConversation) request() anthropic.BetaMessageNewParams {
	return anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(c.provider.model),
		MaxTokens: maxAnswerTokens,
		System:    []anthropic.BetaTextBlockParam{{Text: c.system}},
		Messages:  c.messages,
		Tools:     c.tools,
		// Budget questions don't need deep thinking, and medium keeps
		// replies quick.
		OutputConfig: anthropic.BetaOutputConfigParam{
			Effort: anthropic.BetaOutputConfigEffortMedium,
		},
		CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
		// When a safety classifier declines, the API answers with a fallback
		// model instead of the chat just stopping.
		Betas: []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
		Fallbacks: anthropic.BetaFallbacksParamUnion{
			OfDefault: constant.ValueOf[constant.Default](),
		},
	}
}

func turnFromAnthropic(response *anthropic.BetaMessage) Turn {
	var turn Turn
	for _, block := range response.Content {
		switch content := block.AsAny().(type) {
		case anthropic.BetaTextBlock:
			turn.Text += content.Text
		case anthropic.BetaToolUseBlock:
			turn.Calls = append(turn.Calls, ToolCall{
				ID:    content.ID,
				Name:  content.Name,
				Input: json.RawMessage(content.JSON.Input.Raw()),
			})
		}
	}
	switch response.StopReason {
	case anthropic.BetaStopReasonRefusal:
		turn.Text = cmp.Or(turn.Text, refusalText)
		turn.Calls = nil
	case anthropic.BetaStopReasonMaxTokens:
		// Cut-off calls stay: each needs a result, and an input cut short
		// goes back to the model as an error.
		if len(turn.Calls) == 0 {
			turn.Text += cutOffNote
		}
	}
	return turn
}

// explainAnthropicError says what the person can do about the errors they
// can fix themselves.
func explainAnthropicError(err error) error {
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) {
		return fmt.Errorf("can't reach the Anthropic API: %w", err)
	}
	switch apiErr.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("the Anthropic API rejected the credentials (HTTP %d); "+
			"check the API key in Settings: %w", apiErr.StatusCode, err)
	case http.StatusTooManyRequests:
		return fmt.Errorf("the Anthropic API is rate-limiting this key; "+
			"try again in a moment: %w", err)
	default:
		return fmt.Errorf("anthropic: %w", err)
	}
}
