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

func newTestDir(t *testing.T) *Dir {
	t.Helper()
	dir, err := OpenDir(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func getAccount(t *testing.T, dir *Dir, id string) *app.App {
	t.Helper()
	a, err := dir.GetOrCreate(id)
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
	dir := newTestDir(t)
	alice := getAccount(t, dir, "alice")
	bob := getAccount(t, dir, "bob")

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
	dir := newTestDir(t)
	alice := getAccount(t, dir, "alice")
	bob := getAccount(t, dir, "bob")
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

func TestGetOrCreateReturnsTheSameAppEachTime(t *testing.T) {
	dir := newTestDir(t)

	first := getAccount(t, dir, "alice")
	second := getAccount(t, dir, "alice")

	if first != second {
		t.Error("two calls for one account gave different apps")
	}
}

func TestGetOrCreateRejectsUnsafeIDs(t *testing.T) {
	dir := newTestDir(t)
	for _, id := range []string{"", "..", "../alice", "Alice", "-alice", "a/b", "a.b"} {
		t.Run(id, func(t *testing.T) {
			_, err := dir.GetOrCreate(id)

			if !errors.Is(err, ErrBadID) {
				t.Errorf("GetOrCreate(%q) = %v, want ErrBadID", id, err)
			}
		})
	}
}

func TestAccountDirectoryIsPrivate(t *testing.T) {
	root := t.TempDir()
	dir, err := OpenDir(root, nil)
	if err != nil {
		t.Fatal(err)
	}

	createBudget(t, getAccount(t, dir, "alice"), "home")

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
	dir, err := OpenDir(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	getAccount(t, dir, "carol")
	getAccount(t, dir, "alice")
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".trash"), 0o700); err != nil {
		t.Fatal(err)
	}

	ids, err := dir.List()

	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"alice", "carol"}; !slices.Equal(ids, want) {
		t.Errorf("List() = %v, want %v", ids, want)
	}
}
