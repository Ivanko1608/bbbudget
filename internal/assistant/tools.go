package assistant

import (
	"context"
	"encoding/json"
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
}

func connectTools(ctx context.Context, a *app.App, budget string) (*mcpTools, error) {
	server := mcpserver.New(a, mcpserver.Options{
		DefaultBudget: budget,
		Version:       "assistant",
		Source:        store.SourceAssistant,
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
		result := tools.callTool(ctx, call)
		results = append(results, result)
		emit(newToolEvent(call, result))
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

func (tools *mcpTools) callTool(ctx context.Context, call ToolCall) ToolResult {
	arguments := map[string]any{}
	if len(call.Input) > 0 {
		if err := json.Unmarshal(call.Input, &arguments); err != nil {
			return errorResult(call, "the tool input is not valid JSON: "+err.Error())
		}
	}
	params := &mcp.CallToolParams{Name: call.Name, Arguments: arguments}
	result, err := tools.session.CallTool(ctx, params)
	if err != nil {
		return errorResult(call, err.Error())
	}
	return ToolResult{CallID: call.ID, Content: joinText(result), IsError: result.IsError}
}

func errorResult(call ToolCall, reason string) ToolResult {
	return ToolResult{CallID: call.ID, Content: reason, IsError: true}
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
		event.Error = truncate(firstLine(result.Content), maxErrorLen)
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
