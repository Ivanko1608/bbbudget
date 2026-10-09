// Package store persists budgets.
//
// Every write goes through Update, which re-validates the budget, bumps the
// revision and supports optimistic concurrency (expected revision), so the web
// UI and an MCP client editing the same budget can never silently overwrite
// each other.
package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"bbbudget/internal/budget"
)

var (
	ErrNotFound = errors.New("budget not found")
	ErrExists   = errors.New("budget already exists")
	ErrBadID    = errors.New("budget id must be 1-64 lowercase letters, digits or '-', starting with a letter or digit")
)

// ConflictError means the budget changed since the caller read it.
type ConflictError struct{ Expected, Actual int64 }

func (e *ConflictError) Error() string {
	return fmt.Sprintf("budget was changed elsewhere (you have revision %d, current is %d); reload and retry", e.Expected, e.Actual)
}

// Record is a stored budget with its metadata.
type Record struct {
	ID        string        `json:"id"`
	Revision  int64         `json:"revision"`
	UpdatedAt time.Time     `json:"updated_at"`
	Budget    budget.Budget `json:"budget"`
}

// Meta is a Record without the body, for listings.
type Meta struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Revision  int64     `json:"revision"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AnyRevision disables the optimistic check in Update.
const AnyRevision int64 = -1

// Store is implemented by FileStore. Other backends (SQLite, a remote API)
// only need these methods.
type Store interface {
	List(ctx context.Context) ([]Meta, error)
	Get(ctx context.Context, id string) (Record, error)
	Create(ctx context.Context, id string, b budget.Budget) (Record, error)
	// Update reads the current budget, passes it to fn and stores the result.
	// If expected != AnyRevision and differs from the stored revision, it
	// returns *ConflictError without calling fn.
	Update(ctx context.Context, id string, expected int64, fn func(budget.Budget) (budget.Budget, error)) (Record, error)
	Delete(ctx context.Context, id string) error
	// History returns a budget's recent writes, newest first, without their
	// documents. Every Create and Update is recorded with the Change on its
	// context; limit <= 0 means all that are kept.
	History(ctx context.Context, id string, limit int) ([]Entry, error)
	// Snapshot returns the budget as of a revision still in the history, or
	// ErrNoSnapshot.
	Snapshot(ctx context.Context, id string, rev int64) (budget.Budget, error)
}

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ValidID reports whether id is a safe budget id (also safe as a file name).
func ValidID(id string) bool { return idRe.MatchString(id) }
