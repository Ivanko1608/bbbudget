package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"bbbudget/internal/budget"
	"bbbudget/internal/money"
	"bbbudget/internal/store"
)

func spent(date, amount, category string) budget.TransactionInput {
	return budget.TransactionInput{Date: date, Amount: money.MustDecimal(amount), Category: category}
}

func statementRow(date, amount, currency, category, externalID string) budget.TransactionInput {
	row := spent(date, amount, category)
	row.Currency = currency
	row.ExternalID = externalID
	return row
}

var statement = []budget.TransactionInput{
	statementRow("2026-10-03", "40", "", "home", "stmt-1"),
	statementRow("2026-10-04", "100", "USD", "education", "stmt-2"),
}

func mustLog(t *testing.T, a *App, txs ...budget.TransactionInput) *LogResult {
	t.Helper()
	result, err := a.LogTransactions(context.Background(), byAssistant, "home", txs)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustList(t *testing.T, a *App, f TransactionFilter) []budget.Transaction {
	t.Helper()
	txs, err := a.ListTransactions(context.Background(), "home", f)
	if err != nil {
		t.Fatal(err)
	}
	return txs
}

func TestLoggedTransactionsShareABatch(t *testing.T) {
	a := testApp(t)

	result := mustLog(t, a, statement...)

	if result.Batch != "b1" {
		t.Errorf("batch: %s", result.Batch)
	}
	if len(result.Logged) != 2 {
		t.Fatalf("logged: %+v", result.Logged)
	}
	first := result.Logged[0]
	if first.ID != "t1" {
		t.Errorf("first id: %s", first.ID)
	}
	if first.Batch != "b1" {
		t.Errorf("first batch: %s", first.Batch)
	}
	if first.Source != string(store.SourceMCP) {
		t.Errorf("source: %s", first.Source)
	}
}

func TestASecondCallStartsTheNextBatch(t *testing.T) {
	a := testApp(t)
	mustLog(t, a, statement...)

	result := mustLog(t, a, spent("2026-10-05", "12", "home"))

	if result.Batch != "b2" {
		t.Errorf("batch: %s", result.Batch)
	}
	if id := result.Logged[0].ID; id != "t3" {
		t.Errorf("id: %s", id)
	}
}

func TestLoggedInputIsStoredNormalized(t *testing.T) {
	a := testApp(t)
	messy := statementRow(" 2026-10-03 ", "40", " usd", " home ", " stmt-1 ")

	logged := mustLog(t, a, messy).Logged[0]

	cases := []struct{ field, got, want string }{
		{"date", logged.Date, "2026-10-03"},
		{"currency", logged.Currency, "USD"},
		{"category", logged.Category, "home"},
		{"external id", logged.ExternalID, "stmt-1"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.field, c.got, c.want)
		}
	}
}

func TestAForeignAmountIsValuedLikeAPlannedCost(t *testing.T) {
	a := testApp(t)

	result := mustLog(t, a, statement...)

	// 100 USD at 1.08 per EUR plus the 2% payment loss, rounded up.
	if base := result.Logged[1].Base.String(); base != "94.45" {
		t.Errorf("USD base: %s", base)
	}
}

func TestImportingTheSameStatementTwiceLogsNothingNew(t *testing.T) {
	a := testApp(t)
	mustLog(t, a, statement...)

	again := mustLog(t, a, statement...)

	if len(again.Logged) != 0 {
		t.Errorf("logged again: %+v", again.Logged)
	}
	if len(again.Skipped) != 2 {
		t.Errorf("skipped: %v", again.Skipped)
	}
}

func TestALaterRateLeavesLoggedValuesAlone(t *testing.T) {
	a := testApp(t)
	mustLog(t, a, statement...)
	usd := money.MustDecimal("1.5")
	patch := budget.CurrencyPatch{PerBase: &usd}

	_, err := a.SetCurrency(context.Background(), byTest, "home", "USD", patch)

	if err != nil {
		t.Fatal(err)
	}
	education := mustList(t, a, TransactionFilter{Category: "education"})
	if base := education[0].Base.String(); base != "94.45" {
		t.Errorf("base moved with the rate to %s", base)
	}
}

func TestLoggingIsAllOrNothing(t *testing.T) {
	a := testApp(t)
	rows := []budget.TransactionInput{
		spent("2026-10-03", "40", "home"),
		spent("2026-13-01", "5", "nope"), // bad date, unknown category
		spent("2026-10-05", "-3", "home"),
		spent("2026-10-05", "1.001", "home"),
	}

	_, err := a.LogTransactions(context.Background(), byAssistant, "home", rows)

	var invalid *budget.ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("want a validation error, got %v", err)
	}
	if len(invalid.Issues) != 4 {
		t.Errorf("want 4 issues, got %+v", invalid.Issues)
	}
	if path := invalid.Issues[0].Path; !strings.HasPrefix(path, "transactions[1]") {
		t.Errorf("issue paths should point at the row: %s", path)
	}
	if stored := mustList(t, a, TransactionFilter{}); len(stored) != 0 {
		t.Errorf("a rejected log stored %d transactions", len(stored))
	}
}

func TestListingByMonthIncludesItsLastDay(t *testing.T) {
	a := testApp(t)
	mustLog(t, a,
		spent("2026-10-31", "1", "home"),
		spent("2026-11-01", "2", "home"),
	)

	october := mustList(t, a, TransactionFilter{From: "2026-10", To: "2026-10"})

	if len(october) != 1 {
		t.Fatalf("October: %+v", october)
	}
	if october[0].Date != "2026-10-31" {
		t.Errorf("date: %s", october[0].Date)
	}
}

func TestListingRejectsABadDate(t *testing.T) {
	a := testApp(t)

	_, err := a.ListTransactions(context.Background(), "home", TransactionFilter{From: "October"})

	if !errors.Is(err, ErrBadInput) {
		t.Fatalf("bad date: %v", err)
	}
}

// statusOfOctober logs a month of spending against the example budget,
// eight days into October (testApp's today).
func statusOfOctober(t *testing.T) *budget.MonthStatus {
	t.Helper()
	a := testApp(t)
	fun := budget.Category{ID: "fun", Label: "Fun"}
	addFun := []budget.Op{budget.NewOp("add_category", "", fun)}
	if _, err := a.Apply(context.Background(), byTest, "home", store.AnyRevision, addFun); err != nil {
		t.Fatal(err)
	}
	mustLog(t, a,
		spent("2026-10-02", "900", "travel"),  // 600 planned for the holiday
		spent("2026-10-05", "800", "home"),    // on course to overspend
		spent("2026-10-06", "25", "fun"),      // nothing planned
		spent("2026-11-01", "999", "home"),    // another month
		spent("2026-10-01", "3900", "income"), // salary as planned
	)
	status, err := a.MonthStatus(context.Background(), "home", "")
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func TestMonthStatusCountsTheDays(t *testing.T) {
	status := statusOfOctober(t)

	if status.Month != "2026-10" {
		t.Errorf("month: %s", status.Month)
	}
	if status.DaysInMonth != 31 {
		t.Errorf("days: %d", status.DaysInMonth)
	}
	if status.DaysElapsed != 8 {
		t.Errorf("elapsed: %d", status.DaysElapsed)
	}
	if status.TransactionCount != 4 {
		t.Errorf("transactions: %d", status.TransactionCount)
	}
}

func TestMonthStatusStates(t *testing.T) {
	status := statusOfOctober(t)
	states := map[string]budget.SpendingState{}
	for _, c := range status.Categories {
		states[c.Category] = c.State
	}

	cases := map[string]budget.SpendingState{
		"travel": budget.StateOver,
		"home":   budget.StateProjectedOver,
		"fun":    budget.StateUnplanned,
		"income": budget.StateOK,
	}
	for category, want := range cases {
		if states[category] != want {
			t.Errorf("%s: %s, want %s", category, states[category], want)
		}
	}
}

func TestMonthStatusFlagsOverspending(t *testing.T) {
	status := statusOfOctober(t)

	var texts []string
	for _, f := range status.Flags {
		texts = append(texts, f.Text)
	}

	want := "Travel: €900 spent of €600 planned, **€300 over**."
	if !strings.Contains(strings.Join(texts, "\n"), want) {
		t.Errorf("flags: %q", texts)
	}
}

func TestMonthStatusTotals(t *testing.T) {
	status := statusOfOctober(t)

	if s := status.Spent.String(); s != "1725.00" {
		t.Errorf("spent: %s", s)
	}
	if s := status.Income.String(); s != "3900.00" {
		t.Errorf("income: %s", s)
	}
}

func TestMonthStatusOutsideTheWindowIsAnError(t *testing.T) {
	a := testApp(t)

	_, err := a.MonthStatus(context.Background(), "home", "2030-01")

	if !errors.Is(err, ErrBadInput) {
		t.Fatalf("month outside the window: %v", err)
	}
}

// The page iterates over flags and categories, and null broke it.
func TestMonthStatusWithoutFlagsHasEmptyLists(t *testing.T) {
	a := testApp(t)

	status, err := a.MonthStatus(context.Background(), "home", "")

	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"flags":[]`) {
		t.Errorf("flags: %s", raw)
	}
	if strings.Contains(string(raw), `"categories":null`) {
		t.Errorf("categories: %s", raw)
	}
}

func TestUpdatingTheAmountValuesItAgain(t *testing.T) {
	a := testApp(t)
	mustLog(t, a, spent("2026-10-03", "40", "home"))
	amount := money.MustDecimal("50")
	note := "Lidl"
	patch := TransactionPatch{Amount: &amount, Note: &note}

	updated, err := a.UpdateTransaction(context.Background(), "home", "t1", patch)

	if err != nil {
		t.Fatal(err)
	}
	if updated.Base.String() != "50" {
		t.Errorf("base: %s", updated.Base)
	}
	if updated.Note != "Lidl" {
		t.Errorf("note: %s", updated.Note)
	}
	if updated.Batch != "b1" {
		t.Errorf("batch: %s", updated.Batch)
	}
}

func TestDeletingABatchLeavesTheOthers(t *testing.T) {
	a := testApp(t)
	mustLog(t, a, spent("2026-10-03", "40", "home"), spent("2026-10-04", "10", "home"))
	mustLog(t, a, spent("2026-10-05", "100", "education"))

	removed, err := a.DeleteBatch(context.Background(), "home", "b1")

	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Errorf("removed: %d", removed)
	}
	left := mustList(t, a, TransactionFilter{})
	if len(left) != 1 || left[0].ID != "t3" {
		t.Errorf("left: %+v", left)
	}
}

func TestDeletingTransactionsById(t *testing.T) {
	a := testApp(t)
	mustLog(t, a, spent("2026-10-03", "40", "home"), spent("2026-10-04", "10", "home"))

	removed, err := a.DeleteTransactions(context.Background(), "home", []string{"t2"})

	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("removed: %d", removed)
	}
	if left := mustList(t, a, TransactionFilter{}); len(left) != 1 {
		t.Errorf("left: %+v", left)
	}
}

func TestDeletingAnUnknownTransactionChangesNothing(t *testing.T) {
	a := testApp(t)
	mustLog(t, a, spent("2026-10-03", "40", "home"))

	_, err := a.DeleteTransactions(context.Background(), "home", []string{"t1", "t9"})

	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleting a missing transaction: %v", err)
	}
	if left := mustList(t, a, TransactionFilter{}); len(left) != 1 {
		t.Errorf("a failed delete removed something: %+v", left)
	}
}
