package app

import (
	"context"
	"errors"
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
	// Batch is the id these transactions share, such as "b3". DeleteBatch
	// with it takes back the whole call, for example an imported statement.
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
		return nil, NewClientError(ErrBadInput, "no transactions given")
	}
	metadata := logMetadata{at: a.Now().UTC(), source: string(origin.Source)}
	var result LogResult
	appendBatch := func(b budget.Budget, existing []budget.Transaction) ([]budget.Transaction, error) {
		plan, err := budget.Compile(b)
		if err != nil {
			return nil, err
		}
		result, err = buildBatch(plan, existing, inputs, metadata)
		if err != nil {
			return nil, err
		}
		return append(existing, result.Logged...), nil
	}
	if _, err := a.Store.UpdateLedger(ctx, id, appendBatch); err != nil {
		return nil, err
	}
	return &result, nil
}

// logMetadata is when and by whom transactions were logged.
type logMetadata struct {
	at     time.Time
	source string
}

// buildBatch prices inputs into a new batch after existing, skipping the
// external ids existing already has. Any invalid input fails the batch.
func buildBatch(
	plan *budget.Plan,
	existing []budget.Transaction,
	inputs []budget.TransactionInput,
	metadata logMetadata,
) (LogResult, error) {
	seen := externalIDs(existing)
	transactionIDs := newIDSequence(transactionPrefix, listIDs(existing))
	batchIDs := newIDSequence(batchPrefix, listBatchIDs(existing))
	result := LogResult{Batch: batchIDs.nextID(), Logged: []budget.Transaction{}}
	var issues []budget.Issue
	for i, in := range inputs {
		t, problems := plan.NewTransaction(in, fmt.Sprintf("transactions[%d]", i))
		if len(problems) > 0 {
			issues = append(issues, problems...)
			continue
		}
		if t.ExternalID != "" && seen[t.ExternalID] {
			result.Skipped = append(result.Skipped, t.ExternalID)
			continue
		}
		seen[t.ExternalID] = true
		t.ID = transactionIDs.nextID()
		t.Batch = result.Batch
		t.Source = metadata.source
		t.LoggedAt = metadata.at
		result.Logged = append(result.Logged, t)
	}
	if len(issues) > 0 {
		return LogResult{}, &budget.ValidationError{Issues: issues}
	}
	return result, nil
}

func externalIDs(transactions []budget.Transaction) map[string]bool {
	seen := map[string]bool{}
	for _, t := range transactions {
		if t.ExternalID != "" {
			seen[t.ExternalID] = true
		}
	}
	return seen
}

// Transaction ids are "t1", "t2", …; batch ids are "b1", "b2", ….
const (
	transactionPrefix = "t"
	batchPrefix       = "b"
)

// idSequence hands out ids made of a prefix and a counter.
type idSequence struct {
	prefix     string
	nextNumber int
}

// newIDSequence starts after the highest id already taken.
func newIDSequence(prefix string, taken []string) idSequence {
	highest := 0
	for _, id := range taken {
		number, err := strconv.Atoi(strings.TrimPrefix(id, prefix))
		if err == nil {
			highest = max(highest, number)
		}
	}
	return idSequence{prefix: prefix, nextNumber: highest + 1}
}

func (s *idSequence) nextID() string {
	id := s.prefix + strconv.Itoa(s.nextNumber)
	s.nextNumber++
	return id
}

func listIDs(transactions []budget.Transaction) []string {
	ids := make([]string, 0, len(transactions))
	for _, t := range transactions {
		ids = append(ids, t.ID)
	}
	return ids
}

func listBatchIDs(transactions []budget.Transaction) []string {
	batches := make([]string, 0, len(transactions))
	for _, t := range transactions {
		batches = append(batches, t.Batch)
	}
	return batches
}

// TransactionFilter narrows ListTransactions. From and To are inclusive, either a
// day "YYYY-MM-DD" or a month "YYYY-MM" (the whole month).
type TransactionFilter struct {
	From     string
	To       string
	Category string
	Batch    string
	Limit    int
}

// ListTransactions returns matching transactions, newest first.
func (a *App) ListTransactions(
	ctx context.Context, id string, filter TransactionFilter,
) ([]budget.Transaction, error) {
	match, err := filter.matchFunc()
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
	newestFirst := func(x, y budget.Transaction) int { return strings.Compare(y.Date, x.Date) }
	slices.SortStableFunc(found, newestFirst)
	if filter.Limit > 0 && len(found) > filter.Limit {
		found = found[:filter.Limit]
	}
	return found, nil
}

// matchFunc turns the filter into a test for one transaction.
func (f TransactionFilter) matchFunc() (func(budget.Transaction) bool, error) {
	first, err := parseStartDate(f.From)
	if err != nil {
		return nil, err
	}
	last, err := parseEndDate(f.To)
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

// parseStartDate is the day a "YYYY-MM-DD" or "YYYY-MM" bound starts at; "" is
// the beginning of time.
func parseStartDate(bound string) (time.Time, error) {
	if bound == "" {
		return time.Time{}, nil
	}
	if month, err := time.Parse("2006-01", bound); err == nil {
		return month, nil
	}
	return parseDate(bound)
}

// parseEndDate is the day a "YYYY-MM-DD" or "YYYY-MM" bound ends at; "" is
// the end of time.
func parseEndDate(bound string) (time.Time, error) {
	if bound == "" {
		return time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC), nil
	}
	if month, err := time.Parse("2006-01", bound); err == nil {
		return month.AddDate(0, 1, -1), nil
	}
	return parseDate(bound)
}

func parseDate(bound string) (time.Time, error) {
	day, err := time.Parse(time.DateOnly, bound)
	if err != nil {
		return time.Time{}, NewClientError(
			ErrBadInput, "dates are YYYY-MM-DD or YYYY-MM, got %q", bound)
	}
	return day, nil
}

// TransactionPatch changes a transaction; nil fields stay as they are.
type TransactionPatch struct {
	Date     *string        `json:"date,omitempty"`
	Amount   *money.Decimal `json:"amount,omitempty"`
	Currency *string        `json:"currency,omitempty"`
	Category *string        `json:"category,omitempty"`
	Item     *string        `json:"item,omitempty"`
	Note     *string        `json:"note,omitempty"`
}

// changesBaseValue reports whether the patch changes what the transaction is
// worth in the base currency.
func (p TransactionPatch) changesBaseValue() bool {
	return p.Amount != nil || p.Currency != nil || p.Category != nil
}

// UpdateTransaction changes one transaction. Its base value is worked out
// again at today's rates when the amount, currency or category changes.
func (a *App) UpdateTransaction(
	ctx context.Context, id, transactionID string, patch TransactionPatch,
) (*budget.Transaction, error) {
	var updated budget.Transaction
	replaceTransaction := func(
		b budget.Budget, transactions []budget.Transaction,
	) ([]budget.Transaction, error) {
		hasID := func(t budget.Transaction) bool { return t.ID == transactionID }
		i := slices.IndexFunc(transactions, hasID)
		if i < 0 {
			return nil, NewClientError(store.ErrNotFound, "no transaction %q", transactionID)
		}
		plan, err := budget.Compile(b)
		if err != nil {
			return nil, err
		}
		updated, err = applyPatch(plan, transactions[i], patch)
		if err != nil {
			return nil, err
		}
		transactions[i] = updated
		return transactions, nil
	}
	if _, err := a.Store.UpdateLedger(ctx, id, replaceTransaction); err != nil {
		return nil, err
	}
	return &updated, nil
}

// applyPatch returns old with the patch's fields changed. It keeps old's
// base value unless the patch changes the amount, currency or category.
func applyPatch(
	plan *budget.Plan, old budget.Transaction, patch TransactionPatch,
) (budget.Transaction, error) {
	in := budget.TransactionInput{
		Date:       valueOr(patch.Date, old.Date),
		Amount:     valueOr(patch.Amount, old.Amount),
		Currency:   valueOr(patch.Currency, old.Currency),
		Category:   valueOr(patch.Category, old.Category),
		Item:       valueOr(patch.Item, old.Item),
		Note:       valueOr(patch.Note, old.Note),
		ExternalID: old.ExternalID,
	}
	t, issues := plan.NewTransaction(in, "transaction")
	if len(issues) > 0 {
		return budget.Transaction{}, &budget.ValidationError{Issues: issues}
	}
	if !patch.changesBaseValue() {
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
		return 0, NewClientError(ErrBadInput, "give the ids of the transactions to delete")
	}
	return a.removeTransactions(ctx, id, func(
		transactions []budget.Transaction,
	) ([]budget.Transaction, error) {
		return withoutIDs(transactions, ids)
	})
}

// DeleteBatch removes every transaction logged in batch.
func (a *App) DeleteBatch(ctx context.Context, id, batch string) (int, error) {
	if batch == "" {
		return 0, NewClientError(ErrBadInput, "give the batch to delete")
	}
	return a.removeTransactions(ctx, id, func(
		transactions []budget.Transaction,
	) ([]budget.Transaction, error) {
		return withoutBatch(transactions, batch)
	})
}

// removeTransactions stores what remove keeps, and returns how many it
// took out.
func (a *App) removeTransactions(
	ctx context.Context, id string,
	remove func([]budget.Transaction) ([]budget.Transaction, error),
) (int, error) {
	var removed int
	edit := func(_ budget.Budget, transactions []budget.Transaction) ([]budget.Transaction, error) {
		kept, err := remove(transactions)
		removed = len(transactions) - len(kept)
		return kept, err
	}
	_, err := a.Store.UpdateLedger(ctx, id, edit)
	return removed, err
}

func withoutIDs(transactions []budget.Transaction, ids []string) ([]budget.Transaction, error) {
	var missing []string
	for _, transactionID := range ids {
		hasID := func(t budget.Transaction) bool { return t.ID == transactionID }
		if !slices.ContainsFunc(transactions, hasID) {
			missing = append(missing, transactionID)
		}
	}
	if len(missing) > 0 {
		return nil, NewClientError(store.ErrNotFound, "no transaction %s", strings.Join(missing, ", "))
	}
	hasGivenID := func(t budget.Transaction) bool { return slices.Contains(ids, t.ID) }
	return slices.DeleteFunc(slices.Clone(transactions), hasGivenID), nil
}

func withoutBatch(transactions []budget.Transaction, batch string) ([]budget.Transaction, error) {
	inBatch := func(t budget.Transaction) bool { return t.Batch == batch }
	kept := slices.DeleteFunc(slices.Clone(transactions), inBatch)
	if len(kept) == len(transactions) {
		return nil, NewClientError(store.ErrNotFound, "no transactions in batch %q", batch)
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
	status, err := plan.MonthStatus(month, ledger.Transactions, a.Now())
	if errors.Is(err, budget.ErrBadMonth) || errors.Is(err, budget.ErrOutsideWindow) {
		return nil, NewClientErrorFrom(ErrBadInput, err)
	}
	if err != nil {
		return nil, err
	}
	return status, nil
}
