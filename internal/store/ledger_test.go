package store

import (
	"errors"
	"os"
	"testing"

	"bbbudget/internal/budget"
	"bbbudget/internal/money"
)

func TestLedgerPersistsApartFromTheBudget(t *testing.T) {
	s, b := newStore(t)
	s.Create(ctx, "home", b)
	if l, err := s.Ledger(ctx, "home"); err != nil || len(l.Transactions) != 0 {
		t.Fatalf("empty ledger: %+v %v", l, err)
	}
	add := func(_ budget.Budget, txs []budget.Transaction) ([]budget.Transaction, error) {
		return append(txs, budget.Transaction{ID: "t1", Date: "2026-10-01", Amount: money.MustDecimal("5"), Currency: "EUR", Category: "home", Base: money.MustDecimal("5")}), nil
	}
	if l, err := s.UpdateLedger(ctx, "home", add); err != nil || l.Revision != 1 {
		t.Fatalf("update: %+v %v", l, err)
	}
	other, _ := NewFileStore(s.dir) // another process sees it
	if l, _ := other.Ledger(ctx, "home"); len(l.Transactions) != 1 {
		t.Fatalf("other process: %+v", l)
	}
	if r, _ := s.Get(ctx, "home"); r.Revision != 1 {
		t.Fatal("logging a transaction changed the budget's revision")
	}
	if list, _ := s.List(ctx); len(list) != 1 {
		t.Fatalf("the ledger file was listed as a budget: %+v", list)
	}
	if _, err := s.UpdateLedger(ctx, "nope", add); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ledger of a missing budget: %v", err)
	}
	s.Delete(ctx, "home")
	if _, err := os.Stat(s.ledgerPath("home")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("deleting the budget left its transactions behind")
	}
}
