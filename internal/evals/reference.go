package evals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"bbbudget/internal/assistant"
	"bbbudget/internal/settings"
)

const (
	referenceProviderName settings.AssistantProvider = "reference"
	defaultReferenceReply string                     = "Done."
)

// ReferenceProvider is a scripted model that makes a case's reference tool
// calls, one list per turn, then answers with the reference reply. It shows
// a case can be passed, with no model involved.
type ReferenceProvider struct {
	reference [][]ReferenceCall
	reply     string
	// turn counts the messages from the person received so far.
	turn int
}

// NewReferenceProvider returns the scripted model for the case.
func NewReferenceProvider(c Case) *ReferenceProvider {
	reply := c.ReferenceReply
	if reply == "" {
		reply = defaultReferenceReply
	}
	return &ReferenceProvider{reference: c.Reference, reply: reply}
}

func (p *ReferenceProvider) Name() settings.AssistantProvider { return referenceProviderName }

func (p *ReferenceProvider) Model() string { return string(referenceProviderName) }

func (p *ReferenceProvider) Start(assistant.SystemPrompt, []assistant.Tool) assistant.Conversation {
	return p
}

// SendText answers a new message from the person with the next list of calls.
func (p *ReferenceProvider) SendText(context.Context, string) (assistant.Turn, error) {
	if p.turn >= len(p.reference) {
		return assistant.Turn{}, errors.New("more turns than reference lists")
	}
	calls := p.reference[p.turn]
	p.turn++
	if len(calls) == 0 {
		return assistant.Turn{Text: p.reply}, nil
	}
	toolCalls, err := p.toToolCalls(calls)
	return assistant.Turn{Calls: toolCalls}, err
}

// SendResults answers the results of the calls with the reply.
func (p *ReferenceProvider) SendResults(
	_ context.Context, results []assistant.ToolResult,
) (assistant.Turn, error) {
	for _, result := range results {
		if result.IsError {
			return assistant.Turn{}, fmt.Errorf("reference call failed: %s", result.Content)
		}
	}
	return assistant.Turn{Text: p.reply}, nil
}

func (p *ReferenceProvider) toToolCalls(calls []ReferenceCall) ([]assistant.ToolCall, error) {
	toolCalls := make([]assistant.ToolCall, 0, len(calls))
	for i, call := range calls {
		input, err := json.Marshal(call.Input)
		if err != nil {
			return nil, err
		}
		id := fmt.Sprintf("turn%d-call%d", p.turn, i)
		toolCalls = append(toolCalls, assistant.ToolCall{ID: id, Name: call.Tool, Input: input})
	}
	return toolCalls, nil
}
