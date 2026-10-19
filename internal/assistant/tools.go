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

// toolbox is bbbudget's MCP server, connected in-process for one budget.
type toolbox struct {
	session      *mcp.ClientSession
	tools        []Tool
	readOnly     map[string]bool
	instructions string
}

func connectTools(ctx context.Context, a *app.App, budget string) (*toolbox, error) {
	server := mcpserver.New(a, mcpserver.Options{
		DefaultBudget: budget,
		Version:       "assistant",
		Origin:        store.Origin{Source: store.SourceAssistant},
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
		session.Close()
		return nil, err
	}
	box := &toolbox{
		session:      session,
		readOnly:     map[string]bool{},
		instructions: session.InitializeResult().Instructions,
	}
	for _, tool := range listed.Tools {
		box.tools = append(box.tools, toolOf(tool))
		box.readOnly[tool.Name] = isReadOnly(tool)
	}
	return box, nil
}

func toolOf(tool *mcp.Tool) Tool {
	return Tool{Name: tool.Name, Description: tool.Description, Schema: tool.InputSchema}
}

func isReadOnly(tool *mcp.Tool) bool {
	return tool.Annotations != nil && tool.Annotations.ReadOnlyHint
}

func (box *toolbox) close() { box.session.Close() }

// run makes the calls in order and reports each one, and then whether any
// of them changed the budget.
func (box *toolbox) run(ctx context.Context, calls []ToolCall, emit func(Event)) []ToolResult {
	results := make([]ToolResult, 0, len(calls))
	changed := false
	for _, call := range calls {
		result := box.call(ctx, call)
		results = append(results, result)
		emit(Event{Type: EventTool, Tool: call.Name, Detail: detailOf(result)})
		changed = changed || box.wrote(call, result)
	}
	if changed {
		emit(Event{Type: EventChanged})
	}
	return results
}

func (box *toolbox) wrote(call ToolCall, result ToolResult) bool {
	return !result.IsError && !box.readOnly[call.Name]
}

func (box *toolbox) call(ctx context.Context, call ToolCall) ToolResult {
	arguments := map[string]any{}
	if len(call.Input) > 0 {
		if err := json.Unmarshal(call.Input, &arguments); err != nil {
			return failed(call, "the tool input is not valid JSON: "+err.Error())
		}
	}
	params := &mcp.CallToolParams{Name: call.Name, Arguments: arguments}
	result, err := box.session.CallTool(ctx, params)
	if err != nil {
		return failed(call, err.Error())
	}
	return ToolResult{CallID: call.ID, Content: textOf(result), IsError: result.IsError}
}

func failed(call ToolCall, reason string) ToolResult {
	return ToolResult{CallID: call.ID, Content: reason, IsError: true}
}

func textOf(result *mcp.CallToolResult) string {
	var text strings.Builder
	for _, content := range result.Content {
		if part, ok := content.(*mcp.TextContent); ok {
			text.WriteString(part.Text)
		}
	}
	return text.String()
}

// maxDetail is the most of an error the chat shows next to a tool call.
const maxDetail = 200

// detailOf is a short note about a tool call for the chat: the error, or
// the change a write made.
func detailOf(result ToolResult) string {
	if result.IsError {
		return shorten(firstLine(result.Content), maxDetail)
	}
	var write struct {
		Change string `json:"change"`
	}
	if err := json.Unmarshal([]byte(result.Content), &write); err != nil {
		return ""
	}
	return write.Change
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}

func shorten(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}
