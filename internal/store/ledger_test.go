package store

import (
	"errors"
	"os"
	"testing"

	"bbbudget/internal/budget"
	"bbbudget/internal/money"
)

func newStoreWithHome(t *testing.T) *FileStore {
	t.Helper()
	s, b := newStore(t)
	if _, err := s.Create(ctx, byCLI, "home", b); err != nil {
		t.Fatal(err)
	}
	return s
}

func logFive(_ budget.Budget, txs []budget.Transaction) ([]budget.Transaction, error) {
	five := budget.Transaction{
		ID:       "t1",
		Date:     "2026-10-01",
		Amount:   money.MustDecimal("5"),
		Currency: "EUR",
		Category: "home",
		Base:     money.MustDecimal("5"),
	}
	return append(txs, five), nil
}

func TestANewBudgetHasAnEmptyLedger(t *testing.T) {
	s := newStoreWithHome(t)

	ledger, err := s.Ledger(ctx, "home")

	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Transactions) != 0 {
		t.Errorf("transactions: %+v", ledger.Transactions)
	}
}

func TestTheLedgerIsSharedBetweenProcesses(t *testing.T) {
	s := newStoreWithHome(t)
	other, err := NewFileStore(s.dir)
	if err != nil {
		t.Fatal(err)
	}

	ledger, err := s.UpdateLedger(ctx, "home", logFive)

	if err != nil {
		t.Fatal(err)
	}
	if ledger.Revision != 1 {
		t.Errorf("ledger revision: %d", ledger.Revision)
	}
	seen, err := other.Ledger(ctx, "home")
	if err != nil {
		t.Fatal(err)
	}
	if len(seen.Transactions) != 1 {
		t.Errorf("another process sees %d transactions", len(seen.Transactions))
	}
}

func TestLoggingLeavesTheBudgetAlone(t *testing.T) {
	s := newStoreWithHome(t)

	if _, err := s.UpdateLedger(ctx, "home", logFive); err != nil {
		t.Fatal(err)
	}

	r, err := s.Get(ctx, "home")
	if err != nil {
		t.Fatal(err)
	}
	if r.Revision != 1 {
		t.Errorf("logging changed the budget's revision to %d", r.Revision)
	}
	list, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Errorf("the ledger file was listed as a budget: %+v", list)
	}
}

func TestAMissingBudgetHasNoLedger(t *testing.T) {
	s := newStoreWithHome(t)

	_, err := s.UpdateLedger(ctx, "nope", logFive)

	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("ledger of a missing budget: %v", err)
	}
}

func TestDeletingABudgetDeletesItsLedger(t *testing.T) {
	s := newStoreWithHome(t)
	if _, err := s.UpdateLedger(ctx, "home", logFive); err != nil {
		t.Fatal(err)
	}

	if err := s.Delete(ctx, "home"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(s.ledgerPath("home")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleting the budget left its transactions behind: %v", err)
	}
}
