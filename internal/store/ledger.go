package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"bbbudget/internal/budget"
)

func (s *FileStore) ledgerPath(id string) string { return filepath.Join(s.dir, id+".ledger.json") }

func (s *FileStore) readLedger(id string) (Ledger, error) {
	raw, err := os.ReadFile(s.ledgerPath(id))
	if errors.Is(err, fs.ErrNotExist) {
		return Ledger{Transactions: []budget.Transaction{}}, nil
	}
	if err != nil {
		return Ledger{}, err
	}
	var l Ledger
	if err := json.Unmarshal(raw, &l); err != nil {
		return Ledger{}, fmt.Errorf("transactions of %s are corrupt: %w", id, err)
	}
	if l.Transactions == nil {
		l.Transactions = []budget.Transaction{}
	}
	return l, nil
}

func (s *FileStore) Ledger(ctx context.Context, id string) (l Ledger, err error) {
	err = s.withLock(false, func() error {
		if _, err := s.read(id); err != nil {
			return err
		}
		l, err = s.readLedger(id)
		return err
	})
	return l, err
}

func (s *FileStore) UpdateLedger(ctx context.Context, id string, fn func(budget.Budget, []budget.Transaction) ([]budget.Transaction, error)) (l Ledger, err error) {
	err = s.withLock(true, func() error {
		cur, err := s.read(id)
		if err != nil {
			return err
		}
		old, err := s.readLedger(id)
		if err != nil {
			return err
		}
		txs, err := fn(cur.Budget, append([]budget.Transaction{}, old.Transactions...))
		if err != nil {
			return err
		}
		if len(txs) > budget.MaxTransactions {
			return fmt.Errorf("at most %d transactions per budget", budget.MaxTransactions)
		}
		l = Ledger{Revision: old.Revision + 1, UpdatedAt: s.now().UTC(), Transactions: txs}
		raw, err := json.MarshalIndent(l, "", " ")
		if err != nil {
			return err
		}
		return s.writeFile(s.ledgerPath(id), append(raw, '\n'))
	})
	return l, err
}
