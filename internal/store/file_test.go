package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"bbbudget/internal/budget"
	"bbbudget/internal/money"
	"bbbudget/internal/templates"
)

var ctx = context.Background()

func newStore(t *testing.T) (*FileStore, budget.Budget) {
	t.Helper()
	s, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := templates.Builtin().New("example", "Home", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return s, b
}

func TestCRUD(t *testing.T) {
	s, b := newStore(t)
	r, err := s.Create(ctx, "home", b)
	if err != nil || r.Revision != 1 {
		t.Fatalf("create: %+v %v", r, err)
	}
	if _, err := s.Create(ctx, "home", b); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate create: %v", err)
	}
	got, err := s.Get(ctx, "home")
	if err != nil || got.Budget.Items[1].Amount.String() != "1150" {
		t.Fatalf("get: %v", err)
	}
	list, _ := s.List(ctx)
	if len(list) != 1 || list[0].Name != "Home" {
		t.Fatalf("list: %+v", list)
	}
	if err := s.Delete(ctx, "home"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "home"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestRejectsUnsafeIDs(t *testing.T) {
	s, b := newStore(t)
	for _, id := range []string{"", "../etc", "Home", "a/b", ".hidden", "x.json"} {
		if _, err := s.Create(ctx, id, b); !errors.Is(err, ErrBadID) {
			t.Errorf("%q accepted: %v", id, err)
		}
	}
}

func TestOptimisticConcurrency(t *testing.T) {
	s, b := newStore(t)
	s.Create(ctx, "home", b)
	set := func(amount string) func(budget.Budget) (budget.Budget, error) {
		return func(b budget.Budget) (budget.Budget, error) {
			v := money.MustDecimal(amount)
			return budget.Apply(b, budget.NewOp("set_item", "rent", budget.ItemPatch{Amount: &v}))
		}
	}
	if _, err := s.Update(ctx, "home", 1, set("1200")); err != nil {
		t.Fatal(err)
	}
	_, err := s.Update(ctx, "home", 1, set("1300")) // stale revision
	var ce *ConflictError
	if !errors.As(err, &ce) || ce.Actual != 2 {
		t.Fatalf("expected conflict, got %v", err)
	}
	r, _ := s.Get(ctx, "home")
	if r.Budget.Items[1].Amount.String() != "1200" {
		t.Fatal("stale write went through")
	}
}

func TestNeverStoresInvalidBudget(t *testing.T) {
	s, b := newStore(t)
	s.Create(ctx, "home", b)
	_, err := s.Update(ctx, "home", AnyRevision, func(b budget.Budget) (budget.Budget, error) {
		b.Items[0].Currency = "XXX"
		return b, nil
	})
	if err == nil {
		t.Fatal("invalid budget stored")
	}
	r, _ := s.Get(ctx, "home")
	if r.Revision != 1 || r.Budget.Items[0].Currency != "EUR" {
		t.Fatal("store changed after a rejected write")
	}
	entries, _ := os.ReadDir(s.dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// Two stores on one directory stand in for the web server and the MCP
// server running as separate processes. No increment may be lost.
func TestConcurrentWritersLoseNothing(t *testing.T) {
	a, b := newStore(t)
	other, _ := NewFileStore(a.dir)
	a.Create(ctx, "home", b)
	const n = 40
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := a
			if i%2 == 1 {
				s = other
			}
			_, err := s.Update(ctx, "home", AnyRevision, func(b budget.Budget) (budget.Budget, error) {
				b.Items = append(b.Items, budget.Item{ID: fmt.Sprintf("x%d", i), Name: "x", Amount: money.MustDecimal("1"),
					Currency: "EUR", Category: "home", Schedule: budget.Monthly()})
				return b, nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	r, _ := a.Get(ctx, "home")
	if r.Revision != n+1 || len(r.Budget.Items) != 8+n {
		t.Fatalf("revision %d, %d items; expected %d, %d", r.Revision, len(r.Budget.Items), n+1, 8+n)
	}
}
