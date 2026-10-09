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

func tx(date, amount, currency, category, ext string) budget.TransactionInput {
	return budget.TransactionInput{Date: date, Amount: money.MustDecimal(amount), Currency: currency, Category: category, ExternalID: ext}
}

func TestLogTransactionsValuesAndDedupes(t *testing.T) {
	a := testApp(t)
	ctx := store.WithChange(context.Background(), store.Change{Source: "mcp"})
	res, err := a.LogTransactions(ctx, "home", []budget.TransactionInput{
		tx("2026-10-03", "40", "", "home", "stmt-1"),
		tx("2026-10-04", "100", "USD", "education", "stmt-2"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Logged) != 2 || res.Batch != "b1" || res.Logged[0].ID != "t1" || res.Logged[0].Source != "mcp" {
		t.Fatalf("logged: %+v", res)
	}
	// 100 USD at 1.08 per EUR plus the 2% payment loss, rounded up: 94.45.
	if b := res.Logged[1].Base.String(); b != "94.45" {
		t.Errorf("USD base: %s", b)
	}
	// The same statement again: nothing new, both skipped.
	res, err = a.LogTransactions(ctx, "home", []budget.TransactionInput{tx("2026-10-03", "40", "", "home", "stmt-1"), tx("2026-10-04", "100", "USD", "education", "stmt-2")})
	if err != nil || len(res.Logged) != 0 || len(res.Skipped) != 2 {
		t.Fatalf("re-import: %+v %v", res, err)
	}
	// A rate change later doesn't move what was already spent.
	usd := money.MustDecimal("1.5")
	if _, err := a.SetCurrency(ctx, "home", "USD", budget.CurrencyPatch{PerBase: &usd}); err != nil {
		t.Fatal(err)
	}
	list, _ := a.ListTransactions(ctx, "home", TxFilter{Category: "education"})
	if len(list) != 1 || list[0].Base.String() != "94.45" {
		t.Fatalf("base moved with the rate: %+v", list)
	}
}

func TestLogTransactionsIsAllOrNothing(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	_, err := a.LogTransactions(ctx, "home", []budget.TransactionInput{
		tx("2026-10-03", "40", "", "home", ""),
		tx("2026-13-01", "5", "", "nope", ""),  // bad date, unknown category
		tx("2026-10-05", "-3", "", "home", ""), // not positive
		tx("2026-10-05", "1.001", "", "home", ""),
	})
	var ve *budget.ValidationError
	if !errors.As(err, &ve) || len(ve.Issues) != 4 {
		t.Fatalf("want 4 issues, got %v", err)
	}
	if !strings.HasPrefix(ve.Issues[0].Path, "transactions[1]") {
		t.Errorf("issue paths should point at the row: %+v", ve.Issues)
	}
	if list, _ := a.ListTransactions(ctx, "home", TxFilter{}); len(list) != 0 {
		t.Fatal("a rejected log stored something")
	}
}

func TestMonthStatus(t *testing.T) {
	a := testApp(t) // today is 2026-10-08, the budget starts 2026-10
	ctx := context.Background()
	a.Apply(ctx, "home", store.AnyRevision, []budget.Op{budget.NewOp("add_category", "", budget.Category{ID: "fun", Label: "Fun"})})
	if _, err := a.LogTransactions(ctx, "home", []budget.TransactionInput{
		tx("2026-10-02", "900", "", "travel", ""),  // holiday 600 planned: over
		tx("2026-10-05", "800", "", "home", ""),    // on course to overspend
		tx("2026-10-06", "25", "", "fun", ""),      // nothing planned
		tx("2026-11-01", "999", "", "home", ""),    // another month
		tx("2026-10-01", "3900", "", "income", ""), // salary as planned
	}); err != nil {
		t.Fatal(err)
	}
	st, err := a.MonthStatus(ctx, "home", "")
	if err != nil {
		t.Fatal(err)
	}
	if st.Month != "2026-10" || st.Days != 31 || st.Elapsed != 8 || st.Transactions != 4 {
		t.Fatalf("month: %+v", st)
	}
	state := map[string]string{}
	for _, c := range st.Categories {
		state[c.Category] = c.State
	}
	if state["travel"] != "over" || state["home"] != "pace" || state["fun"] != "unplanned" || state["income"] != "ok" {
		t.Fatalf("states: %v", state)
	}
	var flags []string
	for _, f := range st.Flags {
		flags = append(flags, f.Text)
	}
	if !strings.Contains(strings.Join(flags, "\n"), "Travel: €900 spent of €600 planned, **€300 over**.") {
		t.Errorf("flags: %q", flags)
	}
	if st.Spent.String() != "1725.00" || st.Income.String() != "3900.00" {
		t.Errorf("totals: spent %s, income %s", st.Spent, st.Income)
	}
	if _, err := a.MonthStatus(ctx, "home", "2030-01"); !errors.Is(err, ErrBadInput) {
		t.Errorf("month outside the window: %v", err)
	}
}

func TestUpdateAndDeleteTransactions(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	a.LogTransactions(ctx, "home", []budget.TransactionInput{tx("2026-10-03", "40", "", "home", ""), tx("2026-10-04", "10", "", "home", "")})
	a.LogTransactions(ctx, "home", []budget.TransactionInput{tx("2026-10-05", "100", "USD", "education", "")})
	amt := money.MustDecimal("50")
	note := "Lidl"
	got, err := a.UpdateTransaction(ctx, "home", "t1", TxPatch{Amount: &amt, Note: &note})
	if err != nil || got.Base.String() != "50" || got.Note != "Lidl" || got.Batch != "b1" {
		t.Fatalf("update: %+v %v", got, err)
	}
	if n, err := a.DeleteTransactions(ctx, "home", nil, "b1"); err != nil || n != 2 {
		t.Fatalf("delete batch: %d %v", n, err)
	}
	if _, err := a.DeleteTransactions(ctx, "home", []string{"t1"}, ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleting a missing transaction: %v", err)
	}
	if list, _ := a.ListTransactions(ctx, "home", TxFilter{}); len(list) != 1 || list[0].ID != "t3" {
		t.Fatalf("left: %+v", list)
	}
}

// A month with nothing to flag must say so with an empty list: the page
// iterates over flags, and null broke it.
func TestMonthStatusWithoutFlagsIsAnEmptyList(t *testing.T) {
	a := testApp(t)
	st, err := a.MonthStatus(context.Background(), "home", "")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(st)
	if !strings.Contains(string(raw), `"flags":[]`) || strings.Contains(string(raw), `"categories":null`) {
		t.Fatalf("status JSON: %s", raw)
	}
}
