package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"bbbudget/internal/app"
	"bbbudget/internal/mcpserver"
	"bbbudget/internal/store"
)

// mcpTools is bbbudget's MCP server, connected in-process for one budget.
type mcpTools struct {
	session      *mcp.ClientSession
	tools        []Tool
	readOnly     map[string]bool
	instructions string
	logger       *slog.Logger
}

func connectTools(
	ctx context.Context, a *app.App, budget string, logger *slog.Logger,
) (*mcpTools, error) {
	server := mcpserver.New(a, mcpserver.Options{
		DefaultBudget: budget,
		Version:       "assistant",
		Source:        store.SourceAssistant,
		Log:           logger,
	})
	clientEnd, serverEnd := mcp.NewInMemoryTransports()
	// The connection outlives the request that opened it.
	connection := context.WithoutCancel(ctx)
	if _, err := server.Connect(connection, serverEnd, nil); err != nil {
		return nil, err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "bbbudget-assistant"}, nil)
	session, err := client.Connect(connection, clientEnd, nil)
	if err != nil {
		return nil, err
	}
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	tools := &mcpTools{
		session:      session,
		readOnly:     map[string]bool{},
		instructions: session.InitializeResult().Instructions,
		logger:       logger,
	}
	for _, tool := range listed.Tools {
		tools.tools = append(tools.tools, toTool(tool))
		tools.readOnly[tool.Name] = isReadOnly(tool)
	}
	return tools, nil
}

func toTool(tool *mcp.Tool) Tool {
	return Tool{Name: tool.Name, Description: tool.Description, Schema: tool.InputSchema}
}

func isReadOnly(tool *mcp.Tool) bool {
	return tool.Annotations != nil && tool.Annotations.ReadOnlyHint
}

func (tools *mcpTools) close() { _ = tools.session.Close() }

// runCalls makes the calls in order and reports each one, and then whether any
// of them changed the budget.
func (tools *mcpTools) runCalls(
	ctx context.Context, calls []ToolCall, emit func(Event),
) []ToolResult {
	results := make([]ToolResult, 0, len(calls))
	changed := false
	for _, call := range calls {
		result, event := tools.callTool(ctx, call)
		results = append(results, result)
		emit(event)
		changed = changed || tools.changedBudget(call, result)
	}
	if changed {
		emit(Event{Type: EventChanged})
	}
	return results
}

func (tools *mcpTools) changedBudget(call ToolCall, result ToolResult) bool {
	return !result.IsError && !tools.readOnly[call.Name]
}

// callTool makes one call and returns its result for the model and the event
// for the chat.
func (tools *mcpTools) callTool(ctx context.Context, call ToolCall) (ToolResult, Event) {
	arguments := map[string]any{}
	if len(call.Input) > 0 {
		if err := json.Unmarshal(call.Input, &arguments); err != nil {
			tools.logger.Info("assistant tool input rejected", "tool", call.Name, "err", err)
			return newFailedCall(call, toolInputInvalidMessage)
		}
	}

	params := &mcp.CallToolParams{Name: call.Name, Arguments: arguments}
	reply, err := tools.session.CallTool(ctx, params)
	if err != nil {
		tools.logger.Error("assistant tool call failed", "tool", call.Name, "err", err)
		return newFailedCall(call, toolCallFailedMessage)
	}

	result := ToolResult{CallID: call.ID, Content: joinText(reply), IsError: reply.IsError}

	return result, newToolEvent(call, result)
}

const (
	toolInputInvalidMessage = "the tool input is not valid JSON"
	toolCallFailedMessage   = "the tool call failed; see the server log"
)

// newFailedCall reports a call that failed before the MCP server answered.
func newFailedCall(call ToolCall, message string) (ToolResult, Event) {
	result := ToolResult{CallID: call.ID, Content: message, IsError: true}
	event := Event{Type: EventTool, Tool: call.Name, Error: message}

	return result, event
}

// parseErrorMessage returns the message of a failed tool result. The MCP
// server translates every error into a fixed message, so content in any other
// shape is not shown.
func parseErrorMessage(content string) string {
	var failure struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(content), &failure); err != nil {
		return toolCallFailedMessage
	}
	return cmp.Or(failure.Error, toolCallFailedMessage)
}

func joinText(result *mcp.CallToolResult) string {
	var text strings.Builder
	for _, content := range result.Content {
		if part, ok := content.(*mcp.TextContent); ok {
			text.WriteString(part.Text)
		}
	}
	return text.String()
}

// maxErrorLen is the most of an error the chat shows, in characters.
const maxErrorLen = 200

func newToolEvent(call ToolCall, result ToolResult) Event {
	event := Event{Type: EventTool, Tool: call.Name}
	if result.IsError {
		event.Error = truncate(firstLine(parseErrorMessage(result.Content)), maxErrorLen)
	} else {
		event.Change = parseChange(result.Content)
	}
	return event
}

// parseChange returns the change a write tool reports; read tools report
// none.
func parseChange(content string) string {
	var write struct {
		Change string `json:"change"`
	}
	if err := json.Unmarshal([]byte(content), &write); err != nil {
		return ""
	}
	return write.Change
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}
