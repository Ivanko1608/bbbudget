// Package assistant is the AI chat inside the app. The agent loop runs on the
// server: the browser sends a message and receives events, and only the
// server talks to the model provider. The tools are bbbudget's own MCP
// server, connected in-process, so chat and external MCP clients share one
// set of tools and can't drift apart. Conversations live in memory only.
package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"bbbudget/internal/app"
	"bbbudget/internal/settings"
)

// Tool is one tool the model may call.
type Tool struct {
	Name        string
	Description string
	// Schema is the JSON Schema of the tool's input, as decoded JSON.
	Schema any
}

// ToolCall is a call the model asked for.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// ToolResult answers the ToolCall with the same ID.
type ToolResult struct {
	CallID  string
	Content string
	IsError bool
}

// Turn is one model response: text for the person, tool calls to run, or both.
type Turn struct {
	Text  string
	Calls []ToolCall
}

// Conversation is one chat with a model. Each provider keeps the history in
// its own native form, so nothing the API needs back is lost in translation.
type Conversation interface {
	// SendText sends the person's message.
	SendText(ctx context.Context, text string) (Turn, error)
	// SendResults sends the results of the calls in the previous turn.
	SendResults(ctx context.Context, results []ToolResult) (Turn, error)
}

// Provider starts conversations with one model.
type Provider interface {
	Name() settings.AssistantProvider
	Model() string
	Start(system string, tools []Tool) Conversation
}

// EventType says what an Event is about.
type EventType string

const (
	// EventText is the assistant speaking.
	EventText EventType = "text"
	// EventTool is a tool call, with the tool's name and either the change
	// it made or why it failed.
	EventTool EventType = "tool"
	// EventChanged means the budget was written and the page should reload it.
	EventChanged EventType = "changed"
	// EventError is a reply that failed, with the reason as its error.
	EventError EventType = "error"
	// EventDone ends a reply, with the conversation to continue.
	EventDone EventType = "done"
)

// Event is what the browser receives while the assistant works.
type Event struct {
	Type         EventType `json:"type"`
	Text         string    `json:"text,omitempty"`
	Tool         string    `json:"tool,omitempty"`
	Change       string    `json:"change,omitempty"`
	Error        string    `json:"error,omitempty"`
	Conversation string    `json:"conversation,omitempty"`
}

// MaxToolRounds caps the tool rounds per message, so a confused model can't
// loop forever.
const MaxToolRounds = 25

// ErrTooManyToolRounds means the model was still calling tools after
// MaxToolRounds rounds.
var ErrTooManyToolRounds = errors.New(
	"the assistant used too many tool calls for one message; try a narrower question")

// DefaultTTL is how long an idle conversation is kept.
const DefaultTTL = time.Hour

// Assistant holds the conversations of a running server.
type Assistant struct {
	App *app.App
	// Provider returns the provider configured now; settings can change
	// while the server runs.
	Provider func() (Provider, error)
	// TTL drops conversations idle for longer.
	TTL time.Duration
	Now func() time.Time
	// Log receives the errors of tool calls, which the chat shows only as
	// fixed messages.
	Log *slog.Logger

	mu    sync.Mutex
	chats map[string]*chat
}

// New returns an assistant over the app that keeps idle conversations for
// DefaultTTL.
func New(
	a *app.App, provider func() (Provider, error), logger *slog.Logger,
) *Assistant {
	return &Assistant{
		App:      a,
		Provider: provider,
		Log:      logger,
		TTL:      DefaultTTL,
		Now:      time.Now,
		chats:    map[string]*chat{},
	}
}

// Request is one message from the person.
type Request struct {
	// Conversation continues an earlier conversation; "" starts a new one.
	Conversation string
	Budget       string
	Message      string
}

// Reply runs one message through the assistant, sending events as it goes.
// It returns the id of the conversation, to continue it with.
func (a *Assistant) Reply(ctx context.Context, req Request, emit func(Event)) (string, error) {
	message := strings.TrimSpace(req.Message)
	if message == "" {
		return req.Conversation, app.NewClientError(app.ErrBadInput, "empty message")
	}
	current, err := a.getOrCreateChat(ctx, req.Conversation, req.Budget)
	if err != nil {
		return req.Conversation, err
	}
	// One message at a time per conversation: providers keep a single history.
	current.busy.Lock()
	defer current.busy.Unlock()
	turn, err := current.conversation.SendText(ctx, message)
	if err != nil {
		return current.id, err
	}
	return current.id, current.runToolLoop(ctx, turn, emit)
}

// runToolLoop runs the tools each turn asks for and sends the results
// back, until the model answers without calling any.
func (c *chat) runToolLoop(ctx context.Context, turn Turn, emit func(Event)) error {
	for round := 0; ; round++ {
		if text := strings.TrimSpace(turn.Text); text != "" {
			emit(Event{Type: EventText, Text: text})
		}
		if len(turn.Calls) == 0 {
			return nil
		}
		if round == MaxToolRounds {
			return ErrTooManyToolRounds
		}
		results := c.tools.runCalls(ctx, turn.Calls, emit)
		var err error
		turn, err = c.conversation.SendResults(ctx, results)
		if err != nil {
			return err
		}
	}
}
