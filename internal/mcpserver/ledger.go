package mcpserver

import (
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"bbbudget/internal/app"
	"bbbudget/internal/budget"
)

type LogTransactionsIn struct {
	Ref
	Transactions []budget.TransactionInput `json:"transactions" jsonschema:"what was actually spent or received. date YYYY-MM-DD; amount positive; currency defaults to base; category id (its kind says income or spending); item optional; external_id: a stable id for a statement row, so importing it again is skipped"`
}

type ListTransactionsIn struct {
	Ref
	From     string `json:"from,omitempty" jsonschema:"first day YYYY-MM-DD or month YYYY-MM"`
	To       string `json:"to,omitempty" jsonschema:"last day YYYY-MM-DD or month YYYY-MM"`
	Category string `json:"category,omitempty"`
	Batch    string `json:"batch,omitempty" jsonschema:"only one log call's transactions"`
	Limit    int    `json:"limit,omitempty" jsonschema:"default 50"`
}

type UpdateTransactionIn struct {
	Ref
	ID string `json:"id" jsonschema:"transaction id (t12)"`
	app.TransactionPatch
}

type DeleteTransactionsIn struct {
	Ref
	IDs   []string `json:"ids,omitempty" jsonschema:"transaction ids to remove"`
	Batch string   `json:"batch,omitempty" jsonschema:"remove everything one log call added (e.g. a mistaken import)"`
}

type StatusIn struct {
	Ref
	Month string `json:"month,omitempty" jsonschema:"YYYY-MM; default the current month"`
}

// deletedResult is what deleting transactions returns.
type deletedResult struct {
	Deleted int `json:"deleted"`
}

// loggedResult is what logging transactions returns: the ledger's answer,
// and the flags of each month the transactions fall in.
type loggedResult struct {
	*app.LogResult
	Flags map[string][]string `json:"flags,omitempty"`
}

type statusRow struct {
	Category  string               `json:"category"`
	Label     string               `json:"label"`
	Kind      string               `json:"kind"`
	State     budget.SpendingState `json:"state"`
	Planned   string               `json:"planned"`
	Actual    string               `json:"actual"`
	Remaining string               `json:"remaining"`
	Projected string               `json:"projected,omitempty"`
}

type statusOut struct {
	Month         string      `json:"month"`
	Currency      string      `json:"currency"`
	Days          int         `json:"days"`
	Elapsed       int         `json:"elapsed"`
	PlannedSpend  string      `json:"planned_spending"`
	Spent         string      `json:"spent"`
	PlannedIncome string      `json:"planned_income"`
	Income        string      `json:"income"`
	Transactions  int         `json:"transactions"`
	Categories    []statusRow `json:"categories"`
	Flags         []string    `json:"flags"`
}

func toStatusOut(s *budget.MonthStatus) statusOut {
	out := statusOut{
		Month:         s.Month,
		Currency:      s.Spent.Currency().Code,
		Days:          s.DaysInMonth,
		Elapsed:       s.DaysElapsed,
		PlannedSpend:  s.PlannedSpending.String(),
		Spent:         s.Spent.String(),
		PlannedIncome: s.PlannedIncome.String(),
		Income:        s.Income.String(),
		Transactions:  s.TransactionCount,
		Categories:    []statusRow{},
		Flags:         []string{},
	}
	for _, c := range s.Categories {
		out.Categories = append(out.Categories, newStatusRow(c))
	}
	for _, f := range s.Flags {
		out.Flags = append(out.Flags, plain(f.Text))
	}
	return out
}

func newStatusRow(c budget.CategoryStatus) statusRow {
	row := statusRow{
		Category:  c.Category,
		Label:     c.Label,
		Kind:      c.Kind,
		State:     c.State,
		Planned:   c.Planned.String(),
		Actual:    c.Actual.String(),
		Remaining: c.Remaining.String(),
	}
	if c.Projected != nil {
		row.Projected = c.Projected.String()
	}
	return row
}

// monthStatus is a month's status with its flags in the user's language.
func (h *handlers) monthStatus(ctx context.Context, id, month string) (*budget.MonthStatus, error) {
	status, err := h.app.MonthStatus(ctx, id, month)
	if err != nil {
		return nil, err
	}
	status.Localize(h.localizer())
	return status, nil
}

// flagsByMonth are the flags of every month the transactions fall in.
func (h *handlers) flagsByMonth(
	ctx context.Context, id string, txs []budget.Transaction,
) (map[string][]string, error) {
	months := map[string]bool{}
	for _, t := range txs {
		months[t.Date[:len("YYYY-MM")]] = true
	}
	flags := map[string][]string{}
	for _, month := range slices.Sorted(maps.Keys(months)) {
		status, err := h.monthStatus(ctx, id, month)
		if errors.Is(err, app.ErrBadInput) {
			continue // a month outside the budget's window has no plan to compare with
		}
		if err != nil {
			return nil, err
		}
		if len(status.Flags) > 0 {
			flags[month] = toStatusOut(status).Flags
		}
	}
	return flags, nil
}

func (h *handlers) registerLedger(s *mcp.Server) {
	tool(s, "log_transactions", "Record money actually spent or received (not the plan): one item, or many at once, e.g. from a bank statement the user pasted. Pick each category from get_budget's categories; ask the user when unsure rather than guessing. Give every statement row an external_id so a repeated import is skipped. All or nothing. Returns what was logged, its batch id and the month's flags (overspending, pace, unplanned spending).",
		func(ctx context.Context, in LogTransactionsIn) (any, error) {
			id, err := h.id(in.Budget)
			if err != nil {
				return nil, err
			}
			logged, err := h.app.LogTransactions(ctx, fromAssistant, id, in.Transactions)
			if err != nil {
				return nil, err
			}
			flags, err := h.flagsByMonth(ctx, id, logged.Logged)
			if err != nil {
				return nil, err
			}
			return loggedResult{LogResult: logged, Flags: flags}, nil
		})
	tool(s, "list_transactions", "Logged transactions, newest first, optionally by date range, category or batch.",
		func(ctx context.Context, in ListTransactionsIn) (any, error) {
			id, err := h.id(in.Budget)
			if err != nil {
				return nil, err
			}
			if in.Limit <= 0 {
				in.Limit = 50
			}
			filter := app.TransactionFilter{
				From:     in.From,
				To:       in.To,
				Category: in.Category,
				Batch:    in.Batch,
				Limit:    in.Limit,
			}
			return h.app.ListTransactions(ctx, id, filter)
		})
	tool(s, "update_transaction", "Correct one logged transaction; omitted fields stay. Changing amount, currency or category values it again at today's rate.",
		func(ctx context.Context, in UpdateTransactionIn) (any, error) {
			id, err := h.id(in.Budget)
			if err != nil {
				return nil, err
			}
			return h.app.UpdateTransaction(ctx, id, in.ID, in.TransactionPatch)
		})
	tool(s, "delete_transactions", "Remove logged transactions by id, or a whole batch (everything one log_transactions call added). Use it to take back a mistaken log or import.",
		func(ctx context.Context, in DeleteTransactionsIn) (any, error) {
			id, err := h.id(in.Budget)
			if err != nil {
				return nil, err
			}
			removed, err := h.deleteTransactions(ctx, id, in)
			if err != nil {
				return nil, err
			}
			return deletedResult{Deleted: removed}, nil
		})
	tool(s, "month_status", "Plan versus actual for a month: per category what was planned, spent and is left, the pace so far, and flags for overspending, a pace that will overspend, and spending nothing was planned for. Amounts in the base currency.",
		func(ctx context.Context, in StatusIn) (any, error) {
			id, err := h.id(in.Budget)
			if err != nil {
				return nil, err
			}
			st, err := h.monthStatus(ctx, id, in.Month)
			if err != nil {
				return nil, err
			}
			return toStatusOut(st), nil
		})
}

// deleteTransactions removes the ids or the batch in, whichever it names.
func (h *handlers) deleteTransactions(
	ctx context.Context, id string, in DeleteTransactionsIn,
) (int, error) {
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
