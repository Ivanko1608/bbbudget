package mcpserver

import (
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"bbbudget/internal/app"
	"bbbudget/internal/budget"
	"bbbudget/internal/i18n"
)

const defaultTransactionLimit = 50

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
	Month            string      `json:"month"`
	Currency         string      `json:"currency"`
	DaysInMonth      int         `json:"days_in_month"`
	DaysElapsed      int         `json:"days_elapsed"`
	PlannedSpending  string      `json:"planned_spending"`
	Spent            string      `json:"spent"`
	PlannedIncome    string      `json:"planned_income"`
	Income           string      `json:"income"`
	TransactionCount int         `json:"transaction_count"`
	Categories       []statusRow `json:"categories"`
	Flags            []string    `json:"flags"`
}

func toStatusOut(status *budget.MonthStatus) statusOut {
	out := statusOut{
		Month:            status.Month,
		Currency:         status.Spent.Currency().Code,
		DaysInMonth:      status.DaysInMonth,
		DaysElapsed:      status.DaysElapsed,
		PlannedSpending:  status.PlannedSpending.String(),
		Spent:            status.Spent.String(),
		PlannedIncome:    status.PlannedIncome.String(),
		Income:           status.Income.String(),
		TransactionCount: status.TransactionCount,
		Categories:       []statusRow{},
		Flags:            []string{},
	}
	for _, category := range status.Categories {
		out.Categories = append(out.Categories, newStatusRow(category))
	}
	for _, flag := range status.Flags {
		out.Flags = append(out.Flags, i18n.StripMarkdown(flag.Text))
	}
	return out
}

func newStatusRow(category budget.CategoryStatus) statusRow {
	row := statusRow{
		Category:  category.Category,
		Label:     category.Label,
		Kind:      category.Kind,
		State:     category.State,
		Planned:   category.Planned.String(),
		Actual:    category.Actual.String(),
		Remaining: category.Remaining.String(),
	}
	if category.Projected != nil {
		row.Projected = category.Projected.String()
	}
	return row
}

// getLocalizedMonthStatus is a month's status with its flags in the user's language.
func (h *handlers) getLocalizedMonthStatus(
	ctx context.Context, id, month string,
) (*budget.MonthStatus, error) {
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
	for _, tx := range txs {
		months[tx.Date[:len("YYYY-MM")]] = true
	}
	flags := map[string][]string{}
	for _, month := range slices.Sorted(maps.Keys(months)) {
		status, err := h.getLocalizedMonthStatus(ctx, id, month)
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
	addWriteTool(s, "log_transactions", "Record money actually spent or received (not the plan): one item, or many at once, e.g. from a bank statement the user pasted. Pick each category from get_budget's categories; ask the user when unsure rather than guessing. Give every statement row an external_id so a repeated import is skipped. All or nothing. Returns what was logged, its batch id and the month's flags (overspending, projected overspending, unplanned spending).",
		h.logTransactions)
	addReadTool(s, "list_transactions", "Logged transactions, newest first, optionally by date range, category or batch.",
		h.listTransactions)
	addWriteTool(s, "update_transaction", "Correct one logged transaction; omitted fields stay. Changing amount, currency or category values it again at today's rate.",
		h.updateTransaction)
	addWriteTool(s, "delete_transactions", "Remove logged transactions by id, or a whole batch (everything one log_transactions call added). Use it to take back a mistaken log or import.",
		h.deleteTransactions)
	addReadTool(s, "month_status", "Plan versus actual for a month: per category what was planned, spent and is left, the spending projected to the end of the month, and flags for overspending, projected overspending, and spending nothing was planned for. Amounts in the base currency.",
		h.getMonthStatus)
}

func (h *handlers) logTransactions(ctx context.Context, in LogTransactionsIn) (any, error) {
	id, err := h.id(in.Budget)
	if err != nil {
		return nil, err
	}
	logged, err := h.app.LogTransactions(ctx, h.origin, id, in.Transactions)
	if err != nil {
		return nil, err
	}
	flags, err := h.flagsByMonth(ctx, id, logged.Logged)
	if err != nil {
		return nil, err
	}
	return loggedResult{LogResult: logged, Flags: flags}, nil
}

func (h *handlers) listTransactions(ctx context.Context, in ListTransactionsIn) (any, error) {
	id, err := h.id(in.Budget)
	if err != nil {
		return nil, err
	}
	if in.Limit <= 0 {
		in.Limit = defaultTransactionLimit
	}
	filter := app.TransactionFilter{
		From:     in.From,
		To:       in.To,
		Category: in.Category,
		Batch:    in.Batch,
		Limit:    in.Limit,
	}
	return h.app.ListTransactions(ctx, id, filter)
}

func (h *handlers) updateTransaction(ctx context.Context, in UpdateTransactionIn) (any, error) {
	id, err := h.id(in.Budget)
	if err != nil {
		return nil, err
	}
	return h.app.UpdateTransaction(ctx, id, in.ID, in.TransactionPatch)
}

func (h *handlers) deleteTransactions(
	ctx context.Context, in DeleteTransactionsIn,
) (any, error) {
	id, err := h.id(in.Budget)
	if err != nil {
		return nil, err
	}
	removed, err := h.deleteByIDsOrBatch(ctx, id, in)
	if err != nil {
		return nil, err
	}
	return deletedResult{Deleted: removed}, nil
}

func (h *handlers) getMonthStatus(ctx context.Context, in StatusIn) (any, error) {
	id, err := h.id(in.Budget)
	if err != nil {
		return nil, err
	}
	status, err := h.getLocalizedMonthStatus(ctx, id, in.Month)
	if err != nil {
		return nil, err
	}
	return toStatusOut(status), nil
}

// deleteByIDsOrBatch removes the ids or the batch in, whichever it names.
func (h *handlers) deleteByIDsOrBatch(
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
