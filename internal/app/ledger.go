package app

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"bbbudget/internal/budget"
	"bbbudget/internal/money"
	"bbbudget/internal/store"
)

// LogResult says what LogTransactions did.
type LogResult struct {
	Batch  string               `json:"batch"`
	Logged []budget.Transaction `json:"logged"`
	// Skipped lists external ids already in the ledger (or repeated within
	// this call): importing the same statement twice logs nothing twice.
	Skipped []string `json:"skipped,omitempty"`
}

// LogTransactions validates and stores transactions, all or nothing. Each
// gets its base-currency value at today's rates, and they share a batch id
// so they can be removed together.
func (a *App) LogTransactions(ctx context.Context, id string, ins []budget.TransactionInput) (*LogResult, error) {
	if len(ins) == 0 {
		return nil, fmt.Errorf("%w: no transactions given", ErrBadInput)
	}
	var res LogResult
	_, err := a.Store.UpdateLedger(ctx, id, func(b budget.Budget, txs []budget.Transaction) ([]budget.Transaction, error) {
		res = LogResult{Logged: []budget.Transaction{}}
		p, err := budget.Compile(b)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, t := range txs {
			if t.ExternalID != "" {
				seen[t.ExternalID] = true
			}
		}
		next, batch := nextIDs(txs)
		res.Batch = batch
		var issues []budget.Issue
		now, source := a.Now().UTC(), store.ChangeFrom(ctx).Source
		for i, in := range ins {
			t, iss := p.Value(in, fmt.Sprintf("transactions[%d]", i))
			if len(iss) > 0 {
				issues = append(issues, iss...)
				continue
			}
			if t.ExternalID != "" && seen[t.ExternalID] {
				res.Skipped = append(res.Skipped, t.ExternalID)
				continue
			}
			if t.ExternalID != "" {
				seen[t.ExternalID] = true
			}
			t.ID, t.Batch, t.Source, t.LoggedAt = "t"+strconv.Itoa(next), batch, source, now
			next++
			res.Logged = append(res.Logged, t)
		}
		if len(issues) > 0 {
			return nil, &budget.ValidationError{Issues: issues}
		}
		return append(txs, res.Logged...), nil
	})
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// nextIDs returns the next transaction number and a new batch id.
func nextIDs(txs []budget.Transaction) (int, string) {
	maxT, maxB := 0, 0
	for _, t := range txs {
		if n, err := strconv.Atoi(strings.TrimPrefix(t.ID, "t")); err == nil && n > maxT {
			maxT = n
		}
		if n, err := strconv.Atoi(strings.TrimPrefix(t.Batch, "b")); err == nil && n > maxB {
			maxB = n
		}
	}
	return maxT + 1, "b" + strconv.Itoa(maxB+1)
}

// TxFilter narrows ListTransactions. Dates are inclusive "YYYY-MM-DD"; a
// "YYYY-MM" month means the whole month.
type TxFilter struct {
	From, To string
	Category string
	Batch    string
	Limit    int
}

// ListTransactions returns matching transactions, newest first.
func (a *App) ListTransactions(ctx context.Context, id string, f TxFilter) ([]budget.Transaction, error) {
	l, err := a.Store.Ledger(ctx, id)
	if err != nil {
		return nil, err
	}
	from, to := f.From, f.To
	if len(to) == 7 {
		to += "-31" // string order: every day of the month is <= DD 31
	}
	out := []budget.Transaction{}
	for _, t := range l.Transactions {
		if (from != "" && t.Date < from) || (to != "" && t.Date > to) ||
			(f.Category != "" && t.Category != f.Category) || (f.Batch != "" && t.Batch != f.Batch) {
			continue
		}
		out = append(out, t)
	}
	slices.SortStableFunc(out, func(x, y budget.Transaction) int { return strings.Compare(y.Date, x.Date) })
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

// TxPatch changes a transaction; nil fields stay as they are.
type TxPatch struct {
	Date     *string        `json:"date,omitempty"`
	Amount   *money.Decimal `json:"amount,omitempty"`
	Currency *string        `json:"currency,omitempty"`
	Category *string        `json:"category,omitempty"`
	Line     *string        `json:"line,omitempty"`
	Note     *string        `json:"note,omitempty"`
}

// UpdateTransaction changes one transaction. Its base value is worked out
// again at today's rates when the amount, currency or category changes.
func (a *App) UpdateTransaction(ctx context.Context, id, txID string, p TxPatch) (*budget.Transaction, error) {
	var out budget.Transaction
	_, err := a.Store.UpdateLedger(ctx, id, func(b budget.Budget, txs []budget.Transaction) ([]budget.Transaction, error) {
		i := slices.IndexFunc(txs, func(t budget.Transaction) bool { return t.ID == txID })
		if i < 0 {
			return nil, fmt.Errorf("%w: no transaction %q", store.ErrNotFound, txID)
		}
		t := txs[i]
		in := budget.TransactionInput{Date: t.Date, Amount: t.Amount, Currency: t.Currency, Category: t.Category, Line: t.Line, Note: t.Note, ExternalID: t.ExternalID}
		set := func(dst *string, v *string) {
			if v != nil {
				*dst = *v
			}
		}
		set(&in.Date, p.Date)
		set(&in.Currency, p.Currency)
		set(&in.Category, p.Category)
		set(&in.Line, p.Line)
		set(&in.Note, p.Note)
		if p.Amount != nil {
			in.Amount = *p.Amount
		}
		pl, err := budget.Compile(b)
		if err != nil {
			return nil, err
		}
		nt, issues := pl.Value(in, "transaction")
		if len(issues) > 0 {
			return nil, &budget.ValidationError{Issues: issues}
		}
		keepBase := p.Amount == nil && p.Currency == nil && p.Category == nil
		if keepBase {
			nt.Base = t.Base
		}
		nt.ID, nt.Batch, nt.Source, nt.LoggedAt = t.ID, t.Batch, t.Source, t.LoggedAt
		txs[i], out = nt, nt
		return txs, nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteTransactions removes the given transactions, or a whole batch. It
// returns how many were removed.
func (a *App) DeleteTransactions(ctx context.Context, id string, ids []string, batch string) (int, error) {
	if len(ids) == 0 && batch == "" {
		return 0, fmt.Errorf("%w: give transaction ids or a batch", ErrBadInput)
	}
	removed := 0
	_, err := a.Store.UpdateLedger(ctx, id, func(_ budget.Budget, txs []budget.Transaction) ([]budget.Transaction, error) {
		removed = 0
		want := map[string]bool{}
		for _, x := range ids {
			want[x] = true
		}
		kept := txs[:0]
		for _, t := range txs {
			if want[t.ID] || (batch != "" && t.Batch == batch) {
				removed++
				delete(want, t.ID)
				continue
			}
			kept = append(kept, t)
		}
		if len(want) > 0 {
			missing := make([]string, 0, len(want))
			for x := range want {
				missing = append(missing, x)
			}
			slices.Sort(missing)
			return nil, fmt.Errorf("%w: no transaction %s", store.ErrNotFound, strings.Join(missing, ", "))
		}
		if removed == 0 {
			return nil, fmt.Errorf("%w: no transactions in batch %q", store.ErrNotFound, batch)
		}
		return kept, nil
	})
	return removed, err
}

// MonthStatus is plan versus actual for a month ("YYYY-MM"; "" for the
// current one), with its flags in the caller's language.
func (a *App) MonthStatus(ctx context.Context, id, month string) (*budget.MonthStatus, error) {
	r, err := a.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	l, err := a.Store.Ledger(ctx, id)
	if err != nil {
		return nil, err
	}
	p, err := budget.Compile(r.Budget)
	if err != nil {
		return nil, err
	}
	if month == "" {
		month = a.Now().Format("2006-01")
	}
	st, err := p.Status(month, l.Transactions, a.Now())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadInput, err)
	}
	st.Localize(a.Localizer(ctx))
	return st, nil
}
