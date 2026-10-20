package mcpserver

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"bbbudget/internal/app"
	"bbbudget/internal/store"
)

type WhatsNewIn struct {
	Ref
	FromRevision int64 `json:"from_revision,omitempty" jsonschema:"a revision number; 0 or omitted means your own last change"`
}

// whatsNewResult is what happened to a budget while the assistant wasn't
// looking: who changed what, and what all of it did to the outcome.
type whatsNewResult struct {
	Budget          string        `json:"budget"`
	FromRevision    int64         `json:"from_revision"`
	Revision        int64         `json:"revision"`
	ChangesByOthers []store.Entry `json:"changes_by_others"`
	changeOutcome
	ThisMonth *statusOut `json:"this_month,omitempty"`
}

const whatsNewDescription = `What happened to the budget since your own last change (or ` +
	`from_revision): changes others made (the person in the browser, the command line, ` +
	`rate refreshes), what all of it did to totals, debt payoffs, goal dates and insights, ` +
	`and this month's plan-versus-actual flags. Call it at the start of a conversation ` +
	`about a budget you have worked on before.`

func (h *handlers) registerWhatsNew(s *mcp.Server) {
	addReadTool(s, "whats_new", whatsNewDescription, h.getWhatsNew)
	s.AddResourceTemplate(&mcp.ResourceTemplate{
		Name:        "whats-new",
		URITemplate: "whats-new://{id}",
		MIMEType:    "application/json",
		Description: "What changed in a budget since the assistant's last change, " +
			"and this month's flags (the same as the whats_new tool).",
	}, h.readWhatsNew)
}

func (h *handlers) getWhatsNew(ctx context.Context, in WhatsNewIn) (any, error) {
	id, err := h.id(in.Budget)
	if err != nil {
		return nil, err
	}
	return h.getWhatsNewResult(ctx, id, in.FromRevision)
}

func (h *handlers) readWhatsNew(
	ctx context.Context, req *mcp.ReadResourceRequest,
) (*mcp.ReadResourceResult, error) {
	uri := req.Params.URI
	id := strings.TrimPrefix(uri, "whats-new://")
	result, err := h.getWhatsNewResult(ctx, id, app.FromOwnLastChange)
	if err != nil {
		return nil, mcp.ResourceNotFoundError(uri)
	}
	text, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	contents := &mcp.ResourceContents{URI: uri, MIMEType: "application/json", Text: string(text)}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{contents}}, nil
}

func (h *handlers) getWhatsNewResult(
	ctx context.Context, id string, fromRevision int64,
) (whatsNewResult, error) {
	request := app.WhatsNewRequest{Asker: h.origin.Source, FromRevision: fromRevision}
	news, err := h.app.WhatsNew(ctx, id, request)
	if err != nil {
		return whatsNewResult{}, err
	}
	outcome, err := h.changeOf(news.Before, news.Now.Budget, news.Now.Report)
	if err != nil {
		return whatsNewResult{}, err
	}
	reader := h.localizer()
	app.LocalizeHistory(news.ByOthers, reader)
	result := whatsNewResult{
		Budget:          id,
		FromRevision:    news.FromRevision,
		Revision:        news.Now.Revision,
		ChangesByOthers: news.ByOthers,
		changeOutcome:   outcome,
	}
	if news.ThisMonth != nil {
		news.ThisMonth.Localize(reader)
		thisMonth := toStatusOut(news.ThisMonth)
		result.ThisMonth = &thisMonth
	}
	return result, nil
}
