package mcpserver

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"bbbudget/internal/app"
	"bbbudget/internal/budget"
)

type LogIn struct {
	Ref
	Transactions []budget.TransactionInput `json:"transactions" jsonschema:"what was actually spent or received. date YYYY-MM-DD; amount positive; currency defaults to base; category id (its kind says income or spending); item optional; external_id: a stable id for a statement row, so importing it again is skipped"`
}

type ListTxIn struct {
	Ref
	From     string `json:"from,omitempty" jsonschema:"first day YYYY-MM-DD or month YYYY-MM"`
	To       string `json:"to,omitempty" jsonschema:"last day YYYY-MM-DD or month YYYY-MM"`
	Category string `json:"category,omitempty"`
	Batch    string `json:"batch,omitempty" jsonschema:"only one log call's transactions"`
	Limit    int    `json:"limit,omitempty" jsonschema:"default 50"`
}

type UpdateTxIn struct {
	Ref
	ID string `json:"id" jsonschema:"transaction id (t12)"`
	app.TxPatch
}

type DeleteTxIn struct {
	Ref
	IDs   []string `json:"ids,omitempty" jsonschema:"transaction ids to remove"`
	Batch string   `json:"batch,omitempty" jsonschema:"remove everything one log call added (e.g. a mistaken import)"`
}

// removedResult is what deleting transactions returns.
type removedResult struct {
	Removed int `json:"removed"`
}

func (h *handlers) registerLedger(s *mcp.Server) {
	tool(s, "log_transactions", "Record money actually spent or received (not the plan): one item, or many at once, e.g. from a bank statement the user pasted. Pick each category from get_budget's categories; ask the user when unsure rather than guessing. Give every statement row an external_id so a repeated import is skipped. All or nothing. Returns what was logged and its batch id.",
		func(ctx context.Context, in LogIn) (any, error) {
			id, err := h.id(in.Budget)
			if err != nil {
				return nil, err
			}
			return h.app.LogTransactions(ctx, fromAssistant, id, in.Transactions)
		})
	tool(s, "list_transactions", "Logged transactions, newest first, optionally by date range, category or batch.",
		func(ctx context.Context, in ListTxIn) (any, error) {
			id, err := h.id(in.Budget)
			if err != nil {
				return nil, err
			}
			if in.Limit <= 0 {
				in.Limit = 50
			}
			filter := app.TxFilter{
				From:     in.From,
				To:       in.To,
				Category: in.Category,
				Batch:    in.Batch,
				Limit:    in.Limit,
			}
			return h.app.ListTransactions(ctx, id, filter)
		})
	tool(s, "update_transaction", "Correct one logged transaction; omitted fields stay. Changing amount, currency or category values it again at today's rate.",
		func(ctx context.Context, in UpdateTxIn) (any, error) {
			id, err := h.id(in.Budget)
			if err != nil {
				return nil, err
			}
			return h.app.UpdateTransaction(ctx, id, in.ID, in.TxPatch)
		})
	tool(s, "delete_transactions", "Remove logged transactions by id, or a whole batch (everything one log_transactions call added). Use it to take back a mistaken log or import.",
		func(ctx context.Context, in DeleteTxIn) (any, error) {
			id, err := h.id(in.Budget)
			if err != nil {
				return nil, err
			}
			removed, err := h.deleteTransactions(ctx, id, in)
			if err != nil {
				return nil, err
			}
			return removedResult{Removed: removed}, nil
		})
}

// deleteTransactions removes the ids or the batch in, whichever it names.
func (h *handlers) deleteTransactions(ctx context.Context, id string, in DeleteTxIn) (int, error) {
	byIDs := len(in.IDs) > 0
	byBatch := in.Batch != ""
	switch {
	case byIDs == byBatch:
		return 0, errors.New("give either ids or a batch")
	case byBatch:
		return h.app.DeleteBatch(ctx, id, in.Batch)
	}
	return h.app.DeleteTransactions(ctx, id, in.IDs)
}
