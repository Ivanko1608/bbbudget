package mcpserver

import (
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"bbbudget/internal/app"
	"bbbudget/internal/i18n"
)

// translator turns the errors of a tool, prompt or resource into text that is
// safe to send to an MCP client, and logs the error itself. The SDK would
// otherwise send the text of any handler error as it is, and lower layers
// wrap errors with paths and upstream details.
type translator struct {
	logger *slog.Logger
	// getLocalizer is the language of the client-facing text.
	getLocalizer func() *i18n.Localizer
}

// toToolResult is the result a failed tool call reports to the model: the
// fixed message and its structured fields as JSON, the same body the HTTP API
// sends.
func (t translator) toToolResult(tool string, err error) *mcp.CallToolResult {
	failure, isClientError := app.GetClientFailure(err, t.getLocalizer)
	if isClientError {
		t.logger.Info("mcp request rejected", "operation", tool, "err", err)
	} else {
		t.logger.Error("mcp request failed", "operation", tool, "err", err)
		failure = app.ClientFailure{Message: app.InternalErrorMessage}
	}

	text := &mcp.TextContent{Text: t.encodeFailure(failure)}

	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{text}}
}

// toError is the error a failed prompt or resource read reports to the client.
func (t translator) toError(operation string, err error) error {
	message, isClientError := app.GetClientMessage(err)
	if isClientError {
		t.logger.Info("mcp request rejected", "operation", operation, "err", err)
		return errors.New(message)
	}

	t.logger.Error("mcp request failed", "operation", operation, "err", err)

	return errors.New(app.InternalErrorMessage)
}

func (t translator) encodeFailure(failure app.ClientFailure) string {
	raw, err := json.Marshal(failure.Body())
	if err != nil {
		t.logger.Error("mcp failure body not encodable", "err", err)
		return failure.Message
	}

	return string(raw)
}
