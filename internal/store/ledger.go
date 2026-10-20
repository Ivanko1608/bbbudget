package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

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
	var ledger Ledger
	if err := json.Unmarshal(raw, &ledger); err != nil {
		return Ledger{}, fmt.Errorf("transactions of %s are corrupt: %w", id, err)
	}
	if ledger.Transactions == nil {
		ledger.Transactions = []budget.Transaction{}
	}
	return ledger, nil
}

func (s *FileStore) Ledger(ctx context.Context, id string) (ledger Ledger, err error) {
	err = s.withLock(false, func() error {
		ledger, err = s.ledgerLocked(id)
		return err
	})
	return ledger, err
}

func (s *FileStore) ledgerLocked(id string) (Ledger, error) {
	if _, err := s.read(id); err != nil {
		return Ledger{}, err
	}
	return s.readLedger(id)
}

func (s *FileStore) UpdateLedger(
	ctx context.Context, id string, edit LedgerEditFunc,
) (ledger Ledger, err error) {
	err = s.withLock(true, func() error {
		ledger, err = s.updateLedgerLocked(id, edit)
		return err
	})
	return ledger, err
}

func (s *FileStore) updateLedgerLocked(id string, edit LedgerEditFunc) (Ledger, error) {
	current, err := s.read(id)
	if err != nil {
		return Ledger{}, err
	}
	old, err := s.readLedger(id)
	if err != nil {
		return Ledger{}, err
	}
	txs, err := edit(current.Budget, slices.Clone(old.Transactions))
	if err != nil {
		return Ledger{}, err
	}
	if len(txs) > budget.MaxTransactions {
		return Ledger{}, fmt.Errorf("at most %d transactions per budget", budget.MaxTransactions)
	}
	ledger := Ledger{Revision: old.Revision + 1, UpdatedAt: s.now().UTC(), Transactions: txs}
	raw, err := json.MarshalIndent(ledger, "", " ")
	if err != nil {
		return Ledger{}, err
	}
	return ledger, s.writeFile(s.ledgerPath(id), append(raw, '\n'))
}
