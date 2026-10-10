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
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"bbbudget/internal/app"
	"bbbudget/internal/mcpserver"
)

// Source labels the assistant's writes in the change history.
const Source = "assistant"

// Tool is one tool the model may call.
type Tool struct {
	Name        string
	Description string
	// Schema is the tool's JSON Schema for its input (an object).
	Schema map[string]any
}

// ToolCall is a call the model asked for.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// ToolResult answers a ToolCall.
type ToolResult struct {
	ID      string
	Content string
	IsError bool
}

// Turn is one model response: text for the person, tool calls to run, or both.
type Turn struct {
	Text  string
	Calls []ToolCall
}

// Conversation is one chat with a model. Each provider keeps the history in
// its own native form (so nothing the API needs back, such as thinking
// blocks, is lost in translation). Exactly one of text or results is set.
type Conversation interface {
	Send(ctx context.Context, text string, results []ToolResult) (Turn, error)
}

// Provider starts conversations.
type Provider interface {
	Name() string
	Model() string
	Start(system string, tools []Tool) Conversation
}

// Event is what the browser receives while the assistant works.
type Event struct {
	// Type is "text" (the assistant speaking), "tool" (a tool call, with
	// Tool and a short Detail), "changed" (the budget was written: the page
	// should reload it), "error" or "done".
	Type   string `json:"type"`
	Text   string `json:"text,omitempty"`
	Tool   string `json:"tool,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// maxSteps caps tool rounds per message, so a confused model can't loop forever.
const maxSteps = 25

// Assistant holds the conversations of a running server.
type Assistant struct {
	App      *app.App
	Provider func() (Provider, error)
	// TTL drops conversations idle for longer (default 1 hour).
	TTL time.Duration
	Now func() time.Time

	mu       sync.Mutex
	sessions map[string]*session
	next     int
}

type session struct {
	budget string
	conv   Conversation
	tools  *mcp.ClientSession
	used   time.Time
	busy   sync.Mutex
}

// systemPrompt frames the chat. The MCP server's own instructions (sent with
// it) explain the tools; this adds what is specific to chatting in the app.
const systemPrompt = `You are the assistant inside bbbudget, a household budget planner, talking to the person whose budget it is, in the app itself. They can see the budget on the page next to this chat, and it updates when you change something.

Be brief and concrete: lead with the answer and the numbers that matter, from tool results, never estimated. Use the tools to look things up rather than asking the person for what the budget already says. Use what_if for "what if" questions. Change the budget only when they ask for a change; then say what changed and that it can be undone. When something is ambiguous (which item, which category), ask one short question.

Answer in the language the person writes in. The budget you are working on is %q.`

// Reply runs one message through the assistant, sending events as it goes.
// sessionID "" starts a new conversation; the id in use is returned.
func (a *Assistant) Reply(ctx context.Context, sessionID, budgetID, message string, emit func(Event)) (string, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return sessionID, fmt.Errorf("%w: empty message", app.ErrBadInput)
	}
	s, id, err := a.session(ctx, sessionID, budgetID)
	if err != nil {
		return sessionID, err
	}
	s.busy.Lock() // one message at a time per conversation
	defer s.busy.Unlock()

	turn, err := s.conv.Send(ctx, message, nil)
	for step := 0; ; step++ {
		if err != nil {
			return id, err
		}
		if t := strings.TrimSpace(turn.Text); t != "" {
			emit(Event{Type: "text", Text: t})
		}
		if len(turn.Calls) == 0 {
			break
		}
		if step >= maxSteps {
			return id, errors.New("the assistant used too many tool calls for one message; try a narrower question")
		}
		results := make([]ToolResult, len(turn.Calls))
		changed := false
		for i, c := range turn.Calls {
			res, wrote := a.call(ctx, s.tools, c)
			results[i] = res
			changed = changed || wrote
			emit(Event{Type: "tool", Tool: c.Name, Detail: detail(c, res)})
		}
		if changed {
			emit(Event{Type: "changed"})
		}
		turn, err = s.conv.Send(ctx, "", results)
	}
	return id, nil
}

// readOnly tools never change anything.
var readOnly = map[string]bool{
	"list_budgets": true, "list_templates": true, "get_budget": true, "what_if": true, "compare_scenarios": true,
	"history": true, "list_transactions": true, "month_status": true, "whats_new": true,
}

func (a *Assistant) call(ctx context.Context, cs *mcp.ClientSession, c ToolCall) (ToolResult, bool) {
	var args map[string]any
	if len(c.Input) > 0 {
		if err := json.Unmarshal(c.Input, &args); err != nil {
			return ToolResult{ID: c.ID, Content: "the tool input is not valid JSON: " + err.Error(), IsError: true}, false
		}
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: c.Name, Arguments: args})
	if err != nil {
		return ToolResult{ID: c.ID, Content: err.Error(), IsError: true}, false
	}
	var b strings.Builder
	for _, content := range res.Content {
		if t, ok := content.(*mcp.TextContent); ok {
			b.WriteString(t.Text)
		}
	}
	return ToolResult{ID: c.ID, Content: b.String(), IsError: res.IsError}, !res.IsError && !readOnly[c.Name]
}

// detail is a short item about a tool call for the chat: an error, or the
// change a write made.
func detail(c ToolCall, r ToolResult) string {
	if r.IsError {
		return firstLine(r.Content)
	}
	var out struct {
		Change string `json:"change"`
	}
	if json.Unmarshal([]byte(r.Content), &out) == nil && out.Change != "" {
		return out.Change
	}
	return ""
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

func (a *Assistant) session(ctx context.Context, id, budgetID string) (*session, string, error) {
	now := a.now()
	ttl := a.TTL
	if ttl <= 0 {
		ttl = time.Hour
	}
	a.mu.Lock()
	if a.sessions == nil {
		a.sessions = map[string]*session{}
	}
	for k, s := range a.sessions {
		if now.Sub(s.used) > ttl {
			s.tools.Close()
			delete(a.sessions, k)
		}
	}
	if s, ok := a.sessions[id]; ok && s.budget == budgetID {
		s.used = now
		a.mu.Unlock()
		return s, id, nil
	}
	a.next++
	id = fmt.Sprintf("c%d-%d", now.UnixNano(), a.next)
	a.mu.Unlock()

	if _, err := a.App.Get(ctx, budgetID); err != nil {
		return nil, "", err
	}
	p, err := a.Provider()
	if err != nil {
		return nil, "", err
	}
	// The tools: bbbudget's MCP server, in-process, for this budget.
	srv := mcpserver.New(a.App, mcpserver.Options{DefaultBudget: budgetID, Version: "assistant", Source: Source})
	ct, st := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(context.Background(), st, nil); err != nil {
		return nil, "", err
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "bbbudget-assistant"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		return nil, "", err
	}
	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		cs.Close()
		return nil, "", err
	}
	var tools []Tool
	for _, t := range list.Tools {
		var schema map[string]any
		raw, _ := json.Marshal(t.InputSchema)
		json.Unmarshal(raw, &schema)
		tools = append(tools, Tool{Name: t.Name, Description: t.Description, Schema: schema})
	}
	system := fmt.Sprintf(systemPrompt, budgetID)
	if in := cs.InitializeResult().Instructions; in != "" {
		system += "\n\nAbout the tools:\n" + in
	}
	s := &session{budget: budgetID, conv: p.Start(system, tools), tools: cs, used: now}
	a.mu.Lock()
	a.sessions[id] = s
	a.mu.Unlock()
	return s, id, nil
}

func (a *Assistant) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}
