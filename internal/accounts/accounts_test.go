package accounts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"bbbudget/internal/app"
	"bbbudget/internal/i18n"
	"bbbudget/internal/settings"
	"bbbudget/internal/store"
)

var (
	byTest  = store.Origin{Source: store.SourceCLI}
	english = i18n.For(i18n.Default)
)

func testRegistry(t *testing.T) *Registry {
	t.Helper()
	r, err := NewRegistry(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func open(t *testing.T, r *Registry, id string) *app.App {
	t.Helper()
	a, err := r.Open(id)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func createBudget(t *testing.T, a *app.App, id string) {
	t.Helper()
	if _, err := a.Create(context.Background(), byTest, english, id, id, "blank"); err != nil {
		t.Fatal(err)
	}
}

func budgetIDs(t *testing.T, a *app.App) []string {
	t.Helper()
	metas, err := a.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range metas {
		ids = append(ids, m.ID)
	}
	return ids
}

func TestAccountsSeeOnlyTheirOwnBudgets(t *testing.T) {
	r := testRegistry(t)
	alice, bob := open(t, r, "alice"), open(t, r, "bob")

	createBudget(t, alice, "home")
	createBudget(t, bob, "flat")

	if got := budgetIDs(t, alice); !slices.Equal(got, []string{"home"}) {
		t.Errorf("alice sees %v, want [home]", got)
	}
	if got := budgetIDs(t, bob); !slices.Equal(got, []string{"flat"}) {
		t.Errorf("bob sees %v, want [flat]", got)
	}
}

func TestAccountsKeepTheirOwnSettings(t *testing.T) {
	r := testRegistry(t)
	alice, bob := open(t, r, "alice"), open(t, r, "bob")
	daily := "daily"

	_, err := alice.UpdateSettings(settings.Patch{AutoRefresh: &daily})
	if err != nil {
		t.Fatal(err)
	}

	aliceSettings, err := alice.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if aliceSettings.AutoRefresh != daily {
		t.Errorf("alice auto_refresh = %q, want %q", aliceSettings.AutoRefresh, daily)
	}
	bobSettings, err := bob.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if bobSettings != settings.Defaults() {
		t.Errorf("bob settings = %+v, want defaults", bobSettings)
	}
}

func TestOpenReturnsTheSameAppEachTime(t *testing.T) {
	r := testRegistry(t)

	first, second := open(t, r, "alice"), open(t, r, "alice")

	if first != second {
		t.Error("two opens of one account gave different apps")
	}
}

func TestOpenRejectsUnsafeIDs(t *testing.T) {
	r := testRegistry(t)
	for _, id := range []string{"", "..", "../alice", "Alice", "-alice", "a/b", "a.b"} {
		t.Run(id, func(t *testing.T) {
			_, err := r.Open(id)

			if !errors.Is(err, ErrBadID) {
				t.Errorf("Open(%q) = %v, want ErrBadID", id, err)
			}
		})
	}
}

func TestAccountDirectoryIsPrivate(t *testing.T) {
	root := t.TempDir()
	r, err := NewRegistry(root, nil)
	if err != nil {
		t.Fatal(err)
	}

	createBudget(t, open(t, r, "alice"), "home")

	info, err := os.Stat(filepath.Join(root, "alice", budgetsDir))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("budgets directory mode = %o, want 700", perm)
	}
}

func TestListReturnsAccountsSorted(t *testing.T) {
	root := t.TempDir()
	r, err := NewRegistry(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	open(t, r, "carol")
	open(t, r, "alice")
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".trash"), 0o700); err != nil {
		t.Fatal(err)
	}

	ids, err := r.List()

	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"alice", "carol"}; !slices.Equal(ids, want) {
		t.Errorf("List() = %v, want %v", ids, want)
	}
}
