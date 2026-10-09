package mcpserver

import (
	"context"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"bbbudget/internal/app"
	"bbbudget/internal/budget"
)

type LogIn struct {
	Ref
	Transactions []budget.TransactionInput `json:"transactions" jsonschema:"what was actually spent or received. date YYYY-MM-DD; amount positive; currency defaults to base; category id (its kind says income or spending); line optional; external_id: a stable id for a statement row, so importing it again is skipped"`
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

type StatusIn struct {
	Ref
	Month string `json:"month,omitempty" jsonschema:"YYYY-MM; default the current month"`
}

type txRow struct {
	ID       string `json:"id"`
	Date     string `json:"date"`
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
	Base     string `json:"base"`
	Category string `json:"category"`
	Line     string `json:"line,omitempty"`
	Note     string `json:"note,omitempty"`
	Batch    string `json:"batch"`
}

func txRows(txs []budget.Transaction) []txRow {
	out := make([]txRow, len(txs))
	for i, t := range txs {
		out[i] = txRow{t.ID, t.Date, t.Amount.String(), t.Currency, t.Base.String(), t.Category, t.Line, t.Note, t.Batch}
	}
	return out
}

type statusRow struct {
	Category  string `json:"category"`
	Label     string `json:"label"`
	Kind      string `json:"kind"`
	State     string `json:"state"`
	Planned   string `json:"planned"`
	Actual    string `json:"actual"`
	Remaining string `json:"remaining"`
	Projected string `json:"projected,omitempty"`
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

func statusResult(s *budget.MonthStatus) statusOut {
	out := statusOut{Month: s.Month, Currency: s.Spent.Currency().Code, Days: s.Days, Elapsed: s.Elapsed,
		PlannedSpend: s.Planned.String(), Spent: s.Spent.String(), PlannedIncome: s.IncomePlan.String(), Income: s.Income.String(),
		Transactions: s.Transactions, Categories: []statusRow{}, Flags: []string{}}
	for _, c := range s.Categories {
		r := statusRow{c.Category, c.Label, c.Kind, c.State, c.Planned.String(), c.Actual.String(), c.Remaining.String(), ""}
		if c.Projected != nil {
			r.Projected = c.Projected.String()
		}
		out.Categories = append(out.Categories, r)
	}
	for _, f := range s.Flags {
		out.Flags = append(out.Flags, plain(f.Text))
	}
	return out
}

func (h *handlers) registerLedger(s *mcp.Server) {
	tool(s, "log_transactions", "Record money actually spent or received (not the plan): one item, or many at once, e.g. from a bank statement the user pasted. Pick each category from get_budget's categories; ask the user when unsure rather than guessing. Give every statement row an external_id so a repeated import is skipped. All or nothing. Returns what was logged, its batch id and the month's flags (overspending, pace, unplanned spending).",
		func(ctx context.Context, in LogIn) (any, error) {
			id, err := h.id(in.Budget)
			if err != nil {
				return nil, err
			}
			res, err := h.app.LogTransactions(ctx, id, in.Transactions)
			if err != nil {
				return nil, err
			}
			months := map[string]bool{}
			for _, t := range res.Logged {
				months[t.Date[:7]] = true
			}
			flags := map[string][]string{}
			for _, m := range sortedKeys(months) {
				if st, err := h.app.MonthStatus(ctx, id, m); err == nil && len(st.Flags) > 0 {
					flags[m] = statusResult(st).Flags
				}
			}
			out := map[string]any{"batch": res.Batch, "logged": txRows(res.Logged)}
			if len(res.Skipped) > 0 {
				out["skipped_already_logged"] = res.Skipped
			}
			if len(flags) > 0 {
				out["flags"] = flags
			}
			return out, nil
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
			txs, err := h.app.ListTransactions(ctx, id, app.TxFilter{From: in.From, To: in.To, Category: in.Category, Batch: in.Batch, Limit: in.Limit})
			if err != nil {
				return nil, err
			}
			return txRows(txs), nil
		})
	tool(s, "update_transaction", "Correct one logged transaction; omitted fields stay. Changing amount, currency or category values it again at today's rate.",
		func(ctx context.Context, in UpdateTxIn) (any, error) {
			id, err := h.id(in.Budget)
			if err != nil {
				return nil, err
			}
			t, err := h.app.UpdateTransaction(ctx, id, in.ID, in.TxPatch)
			if err != nil {
				return nil, err
			}
			return txRows([]budget.Transaction{*t})[0], nil
		})
	tool(s, "delete_transactions", "Remove logged transactions by id, or a whole batch (everything one log_transactions call added). Use it to take back a mistaken log or import.",
		func(ctx context.Context, in DeleteTxIn) (any, error) {
			id, err := h.id(in.Budget)
			if err != nil {
				return nil, err
			}
			n, err := h.app.DeleteTransactions(ctx, id, in.IDs, in.Batch)
			if err != nil {
				return nil, err
			}
			return map[string]int{"removed": n}, nil
		})
	tool(s, "month_status", "Plan versus actual for a month: per category what was planned, spent and is left, the pace so far, and flags for overspending, a pace that will overspend, and spending nothing was planned for. Amounts in the base currency.",
		func(ctx context.Context, in StatusIn) (any, error) {
			id, err := h.id(in.Budget)
			if err != nil {
				return nil, err
			}
			st, err := h.app.MonthStatus(ctx, id, in.Month)
			if err != nil {
				return nil, err
			}
			return statusResult(st), nil
		})
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
