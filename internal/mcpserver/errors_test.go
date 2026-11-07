package mcpserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"bbbudget/internal/app"
	"bbbudget/internal/i18n"
	"bbbudget/internal/store"
)

const wrapperPath = "/wrapper/path"

func newTestTranslator(logs *bytes.Buffer) translator {
	return translator{
		logger:       slog.New(slog.NewTextHandler(logs, nil)),
		getLocalizer: func() *i18n.Localizer { return i18n.For("en") },
	}
}

func callFailing(translate translator, failure error) string {
	handle := toMCPHandler("test_tool", translate, func(context.Context, struct{}) (any, error) {
		return nil, failure
	})

	result, _, _ := handle(context.Background(), nil, struct{}{})

	return result.Content[0].(*mcp.TextContent).Text
}

func TestAToolFailureSendsAFixedMessageAndLogsTheError(t *testing.T) {
	logs := &bytes.Buffer{}
	failure := fmt.Errorf("opening %s: %w", wrapperPath, errors.New("permission denied"))

	text := callFailing(newTestTranslator(logs), failure)

	if text != `{"error":"internal error"}` {
		t.Errorf("tool result %s, want the fixed internal error", text)
	}
	if !strings.Contains(logs.String(), wrapperPath) {
		t.Errorf("the full error was not logged: %s", logs)
	}
}

func TestAToolFailureTheClientMayHearAboutKeepsOnlyTheFixedMessage(t *testing.T) {
	logs := &bytes.Buffer{}
	failure := fmt.Errorf("loading %s: %w", wrapperPath, store.ErrNotFound)

	text := callFailing(newTestTranslator(logs), failure)

	if text != `{"error":"budget not found"}` {
		t.Errorf("tool result %s, want the fixed not-found message", text)
	}
	if !strings.Contains(logs.String(), wrapperPath) {
		t.Errorf("the full error was not logged: %s", logs)
	}
}

func TestAResourceFailureSendsAFixedMessage(t *testing.T) {
	logs := &bytes.Buffer{}
	failure := fmt.Errorf("reading %s: %w", wrapperPath, app.ErrRatesOff)

	err := newTestTranslator(logs).toError("budget resource", failure)

	if err.Error() != app.RatesOffMessage {
		t.Errorf("error %q, want %q", err, app.RatesOffMessage)
	}
}
