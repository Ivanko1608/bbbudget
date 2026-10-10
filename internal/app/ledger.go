package app

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

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
func (a *App) LogTransactions(
	ctx context.Context, origin store.Origin, id string, inputs []budget.TransactionInput,
) (*LogResult, error) {
	if len(inputs) == 0 {
		return nil, fmt.Errorf("%w: no transactions given", ErrBadInput)
	}
	stamp := logStamp{at: a.Now().UTC(), source: string(origin.Source)}
	var result LogResult
	logBatch := func(b budget.Budget, existing []budget.Transaction) ([]budget.Transaction, error) {
		plan, err := budget.Compile(b)
		if err != nil {
			return nil, err
		}
		result, err = newBatch(plan, existing, inputs, stamp)
		if err != nil {
			return nil, err
		}
		return append(existing, result.Logged...), nil
	}
	if _, err := a.Store.UpdateLedger(ctx, id, logBatch); err != nil {
		return nil, err
	}
	return &result, nil
}

// logStamp is when and by whom transactions were logged.
type logStamp struct {
	at     time.Time
	source string
}

// newBatch prices inputs into a new batch after existing, skipping the
// external ids existing already has. Any invalid input fails the batch.
func newBatch(
	plan *budget.Plan, existing []budget.Transaction, inputs []budget.TransactionInput, stamp logStamp,
) (LogResult, error) {
	seen := externalIDs(existing)
	next := nextNumber(existing, transactionID)
	result := LogResult{Batch: "b" + strconv.Itoa(nextNumber(existing, batchID)), Logged: []budget.Transaction{}}
	var issues []budget.Issue
	for i, in := range inputs {
		t, problems := plan.Price(in, fmt.Sprintf("transactions[%d]", i))
		if len(problems) > 0 {
			issues = append(issues, problems...)
			continue
		}
		if t.ExternalID != "" && seen[t.ExternalID] {
			result.Skipped = append(result.Skipped, t.ExternalID)
			continue
		}
		seen[t.ExternalID] = true
		t.ID = "t" + strconv.Itoa(next)
		t.Batch = result.Batch
		t.Source = stamp.source
		t.LoggedAt = stamp.at
		next++
		result.Logged = append(result.Logged, t)
	}
	if len(issues) > 0 {
		return LogResult{}, &budget.ValidationError{Issues: issues}
	}
	return result, nil
}

func externalIDs(txs []budget.Transaction) map[string]bool {
	seen := map[string]bool{}
	for _, t := range txs {
		if t.ExternalID != "" {
			seen[t.ExternalID] = true
		}
	}
	return seen
}

func transactionID(t budget.Transaction) string { return strings.TrimPrefix(t.ID, "t") }
func batchID(t budget.Transaction) string       { return strings.TrimPrefix(t.Batch, "b") }

// nextNumber is one more than the highest number number(t) gives.
func nextNumber(txs []budget.Transaction, number func(budget.Transaction) string) int {
	highest := 0
	for _, t := range txs {
		if n, err := strconv.Atoi(number(t)); err == nil {
			highest = max(highest, n)
		}
	}
	return highest + 1
}

// TxFilter narrows ListTransactions. From and To are inclusive, either a
// day "YYYY-MM-DD" or a month "YYYY-MM" (the whole month).
type TxFilter struct {
	From     string
	To       string
	Category string
	Batch    string
	Limit    int
}

// ListTransactions returns matching transactions, newest first.
func (a *App) ListTransactions(
	ctx context.Context, id string, f TxFilter,
) ([]budget.Transaction, error) {
	match, err := f.matcher()
	if err != nil {
		return nil, err
	}
	ledger, err := a.Store.Ledger(ctx, id)
	if err != nil {
		return nil, err
	}
	found := []budget.Transaction{}
	for _, t := range ledger.Transactions {
		if match(t) {
			found = append(found, t)
		}
	}
	slices.SortStableFunc(found, func(x, y budget.Transaction) int { return strings.Compare(y.Date, x.Date) })
	if f.Limit > 0 && len(found) > f.Limit {
		found = found[:f.Limit]
	}
	return found, nil
}

// matcher turns the filter into a test for one transaction.
func (f TxFilter) matcher() (func(budget.Transaction) bool, error) {
	first, err := firstDay(f.From)
	if err != nil {
		return nil, err
	}
	last, err := lastDay(f.To)
	if err != nil {
		return nil, err
	}
	return func(t budget.Transaction) bool {
		day, err := time.Parse(time.DateOnly, t.Date)
		inRange := err == nil && !day.Before(first) && !day.After(last)
		inCategory := f.Category == "" || t.Category == f.Category
		inBatch := f.Batch == "" || t.Batch == f.Batch
		return inRange && inCategory && inBatch
	}, nil
}

// firstDay is the day a "YYYY-MM-DD" or "YYYY-MM" bound starts at; "" is
// the beginning of time.
func firstDay(bound string) (time.Time, error) {
	if bound == "" {
		return time.Time{}, nil
	}
	if month, err := time.Parse("2006-01", bound); err == nil {
		return month, nil
	}
	return parseDay(bound)
}

// lastDay is the day a "YYYY-MM-DD" or "YYYY-MM" bound ends at; "" is
// the end of time.
func lastDay(bound string) (time.Time, error) {
	if bound == "" {
		return time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC), nil
	}
	if month, err := time.Parse("2006-01", bound); err == nil {
		return month.AddDate(0, 1, -1), nil
	}
	return parseDay(bound)
}

func parseDay(bound string) (time.Time, error) {
	day, err := time.Parse(time.DateOnly, bound)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: dates are YYYY-MM-DD or YYYY-MM, got %q", ErrBadInput, bound)
	}
	return day, nil
}

// TxPatch changes a transaction; nil fields stay as they are.
type TxPatch struct {
	Date     *string        `json:"date,omitempty"`
	Amount   *money.Decimal `json:"amount,omitempty"`
	Currency *string        `json:"currency,omitempty"`
	Category *string        `json:"category,omitempty"`
	Item     *string        `json:"item,omitempty"`
	Note     *string        `json:"note,omitempty"`
}

// changesValue reports whether the patch changes what the transaction is
// worth in the base currency.
func (p TxPatch) changesValue() bool {
	return p.Amount != nil || p.Currency != nil || p.Category != nil
}

// UpdateTransaction changes one transaction. Its base value is worked out
// again at today's rates when the amount, currency or category changes.
func (a *App) UpdateTransaction(
	ctx context.Context, id, txID string, patch TxPatch,
) (*budget.Transaction, error) {
	var updated budget.Transaction
	replace := func(b budget.Budget, txs []budget.Transaction) ([]budget.Transaction, error) {
		i := slices.IndexFunc(txs, func(t budget.Transaction) bool { return t.ID == txID })
		if i < 0 {
			return nil, fmt.Errorf("%w: no transaction %q", store.ErrNotFound, txID)
		}
		plan, err := budget.Compile(b)
		if err != nil {
			return nil, err
		}
		updated, err = patched(plan, txs[i], patch)
		if err != nil {
			return nil, err
		}
		txs[i] = updated
		return txs, nil
	}
	if _, err := a.Store.UpdateLedger(ctx, id, replace); err != nil {
		return nil, err
	}
	return &updated, nil
}

// patched is old with patch applied, priced again if its value changed.
func patched(plan *budget.Plan, old budget.Transaction, patch TxPatch) (budget.Transaction, error) {
	in := budget.TransactionInput{
		Date:       valueOr(patch.Date, old.Date),
		Amount:     valueOr(patch.Amount, old.Amount),
		Currency:   valueOr(patch.Currency, old.Currency),
		Category:   valueOr(patch.Category, old.Category),
		Item:       valueOr(patch.Item, old.Item),
		Note:       valueOr(patch.Note, old.Note),
		ExternalID: old.ExternalID,
	}
	t, issues := plan.Price(in, "transaction")
	if len(issues) > 0 {
		return budget.Transaction{}, &budget.ValidationError{Issues: issues}
	}
	if !patch.changesValue() {
		t.Base = old.Base
	}
	t.ID = old.ID
	t.Batch = old.Batch
	t.Source = old.Source
	t.LoggedAt = old.LoggedAt
	return t, nil
}

func valueOr[T any](patch *T, old T) T {
	if patch == nil {
		return old
	}
	return *patch
}

// DeleteTransactions removes the transactions with the given ids, all or
// none: an unknown id is an error.
func (a *App) DeleteTransactions(ctx context.Context, id string, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, fmt.Errorf("%w: give the ids of the transactions to delete", ErrBadInput)
	}
	return a.removeTransactions(ctx, id, func(txs []budget.Transaction) ([]budget.Transaction, error) {
		return withoutIDs(txs, ids)
	})
}

// DeleteBatch removes every transaction logged in batch.
func (a *App) DeleteBatch(ctx context.Context, id, batch string) (int, error) {
	if batch == "" {
		return 0, fmt.Errorf("%w: give the batch to delete", ErrBadInput)
	}
	return a.removeTransactions(ctx, id, func(txs []budget.Transaction) ([]budget.Transaction, error) {
		return withoutBatch(txs, batch)
	})
}

// removeTransactions stores what remove keeps, and returns how many it
// took out.
func (a *App) removeTransactions(
	ctx context.Context, id string,
	remove func([]budget.Transaction) ([]budget.Transaction, error),
) (int, error) {
	var removed int
	keep := func(_ budget.Budget, txs []budget.Transaction) ([]budget.Transaction, error) {
		kept, err := remove(txs)
		removed = len(txs) - len(kept)
		return kept, err
	}
	_, err := a.Store.UpdateLedger(ctx, id, keep)
	return removed, err
}

func withoutIDs(txs []budget.Transaction, ids []string) ([]budget.Transaction, error) {
	var missing []string
	for _, txID := range ids {
		if !slices.ContainsFunc(txs, func(t budget.Transaction) bool { return t.ID == txID }) {
			missing = append(missing, txID)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: no transaction %s", store.ErrNotFound, strings.Join(missing, ", "))
	}
	named := func(t budget.Transaction) bool { return slices.Contains(ids, t.ID) }
	return slices.DeleteFunc(slices.Clone(txs), named), nil
}

func withoutBatch(txs []budget.Transaction, batch string) ([]budget.Transaction, error) {
	inBatch := func(t budget.Transaction) bool { return t.Batch == batch }
	kept := slices.DeleteFunc(slices.Clone(txs), inBatch)
	if len(kept) == len(txs) {
		return nil, fmt.Errorf("%w: no transactions in batch %q", store.ErrNotFound, batch)
	}
	return kept, nil
}

// MonthStatus is plan versus actual for a month ("YYYY-MM"; "" for the
// current one), with its flags in English.
func (a *App) MonthStatus(ctx context.Context, id, month string) (*budget.MonthStatus, error) {
	r, err := a.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	ledger, err := a.Store.Ledger(ctx, id)
	if err != nil {
		return nil, err
	}
	plan, err := budget.Compile(r.Budget)
	if err != nil {
		return nil, err
	}
	if month == "" {
		month = a.Now().Format("2006-01")
	}
	status, err := plan.Status(month, ledger.Transactions, a.Now())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadInput, err)
	}
	return status, nil
}
