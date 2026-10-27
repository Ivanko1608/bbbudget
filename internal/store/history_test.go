package store

import (
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"bbbudget/internal/budget"
	"bbbudget/internal/money"
)

var (
	byCLI       = Origin{Source: SourceCLI}
	byAssistant = Origin{Source: SourceMCP}
	byBrowser   = Origin{Source: SourceWeb}
	byEditor    = Origin{Source: SourceWeb, Autosave: true}
)

func setRent(amount string) EditFunc {
	return func(b budget.Budget) (budget.Budget, error) {
		b.Items[rentIndex(b)].Amount = money.MustDecimal(amount)
		return b, nil
	}
}

func rentIndex(b budget.Budget) int {
	return slices.IndexFunc(b.Items, func(i budget.Item) bool { return i.ID == "rent" })
}

func mustCreate(t testing.TB, s *FileStore, origin Origin, b budget.Budget) {
	t.Helper()
	if _, err := s.Create(ctx, origin, "home", b); err != nil {
		t.Fatal(err)
	}
}

func mustUpdate(t testing.TB, s *FileStore, origin Origin, edit EditFunc) {
	t.Helper()
	if _, err := s.Update(ctx, origin, "home", AnyRevision, edit); err != nil {
		t.Fatal(err)
	}
}

func mustHistory(t *testing.T, s *FileStore) []Entry {
	t.Helper()
	entries, err := s.History(ctx, "home", AllEntries)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func revisions(entries []Entry) []int64 {
	revs := make([]int64, len(entries))
	for i, e := range entries {
		revs[i] = e.Revision
	}
	return revs
}

// rentAt is the rent in the budget as it was at revision.
func rentAt(t *testing.T, s *FileStore, revision int64) string {
	t.Helper()
	b, err := s.Snapshot(ctx, "home", revision)
	if err != nil {
		t.Fatal(err)
	}
	return b.Items[rentIndex(b)].Amount.String()
}

// clock lets a test move time forward between writes.
type clock struct{ now time.Time }

func (c *clock) read() time.Time         { return c.now }
func (c *clock) advance(d time.Duration) { c.now = c.now.Add(d) }

func TestHistoryListsWritesNewestFirst(t *testing.T) {
	s, b := newStore(t)
	mustCreate(t, s, byCLI, b)
	mustUpdate(t, s, byAssistant, setRent("1200"))

	entries := mustHistory(t, s)

	if got, want := revisions(entries), []int64{2, 1}; !slices.Equal(got, want) {
		t.Errorf("revisions %v, want %v", got, want)
	}
	if entries[0].Source != SourceMCP {
		t.Errorf("r2 source: %s", entries[0].Source)
	}
	if entries[1].Source != SourceCLI {
		t.Errorf("r1 source: %s", entries[1].Source)
	}
}

func TestEntryListsWhatChanged(t *testing.T) {
	s, b := newStore(t)
	mustCreate(t, s, byCLI, b)
	mustUpdate(t, s, byAssistant, setRent("1200"))

	entry := mustHistory(t, s)[0]

	rent := budget.Difference{
		Action: budget.Changed,
		Part:   budget.PartItem,
		Name:   "Rent",
		Field:  "amount",
		Old:    "1150",
		New:    "1200",
	}
	if !slices.Equal(entry.Changes, []budget.Difference{rent}) {
		t.Errorf("changes: %+v", entry.Changes)
	}
	if entry.Summary != "Rent: amount 1150 → 1200" {
		t.Errorf("summary: %q", entry.Summary)
	}
	if !entry.Undoable {
		t.Error("a change after the creation can be undone")
	}
}

func TestCreationEntry(t *testing.T) {
	s, b := newStore(t)
	mustCreate(t, s, byCLI, b)

	entry := mustHistory(t, s)[0]

	if entry.Summary != "created" {
		t.Errorf("summary: %q", entry.Summary)
	}
	if entry.Changes != nil {
		t.Errorf("changes: %+v", entry.Changes)
	}
	if entry.Undoable {
		t.Error("the creation has no earlier version to go back to")
	}
}

func TestUndoIsRecordedWithTheRevisionItUndoes(t *testing.T) {
	s, b := newStore(t)
	mustCreate(t, s, byCLI, b)
	mustUpdate(t, s, byAssistant, setRent("1200"))

	mustUpdate(t, s, Origin{Source: SourceMCP, Undoes: 2}, setRent("1150"))

	entry := mustHistory(t, s)[0]
	if entry.Undoes != 2 {
		t.Errorf("undoes: %d", entry.Undoes)
	}
	if entry.Summary != "undid r2: Rent: amount 1200 → 1150" {
		t.Errorf("summary: %q", entry.Summary)
	}
}

func TestSnapshotIsTheBudgetAsItWas(t *testing.T) {
	s, b := newStore(t)
	mustCreate(t, s, byCLI, b)
	mustUpdate(t, s, byAssistant, setRent("1200"))

	if rent := rentAt(t, s, 1); rent != "1150" {
		t.Errorf("rent at r1: %s", rent)
	}
	if rent := rentAt(t, s, 2); rent != "1200" {
		t.Errorf("rent at r2: %s", rent)
	}
	if _, err := s.Snapshot(ctx, "home", 9); !errors.Is(err, ErrNoSnapshot) {
		t.Errorf("a revision never written: %v", err)
	}
}

func TestRejectedWriteLeavesNoHistory(t *testing.T) {
	s, b := newStore(t)
	mustCreate(t, s, byCLI, b)
	invalid := func(b budget.Budget) (budget.Budget, error) {
		b.Items[0].Currency = "XXX"
		return b, nil
	}

	_, err := s.Update(ctx, byCLI, "home", AnyRevision, invalid)

	if err == nil {
		t.Fatal("an invalid budget was saved")
	}
	if got := revisions(mustHistory(t, s)); !slices.Equal(got, []int64{1}) {
		t.Errorf("revisions after a rejected write: %v", got)
	}
}

func TestAutosavesMergeIntoOneEntry(t *testing.T) {
	s, b := newStore(t)
	c := &clock{time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
	s.now = c.read
	mustCreate(t, s, byCLI, b)
	for _, typed := range []string{"1", "12", "120", "1200"} { // r2–r5
		c.advance(time.Second)
		mustUpdate(t, s, byEditor, setRent(typed))
	}

	entries := mustHistory(t, s)

	if got, want := revisions(entries), []int64{5, 1}; !slices.Equal(got, want) {
		t.Errorf("revisions %v, want %v", got, want)
	}
	if entries[0].Summary != "Rent: amount 1150 → 1200" {
		t.Errorf("the run isn't described as one change: %q", entries[0].Summary)
	}
}

func TestAutosavesMergeOnlyWithAutosaves(t *testing.T) {
	cases := []struct {
		name   string
		gap    time.Duration
		origin Origin
	}{
		{"after another source", time.Second, byAssistant},
		{"a deliberate save", time.Second, byBrowser},
		{"after the window", coalesceWindow + time.Second, byEditor},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, b := newStore(t)
			c := &clock{time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
			s.now = c.read
			mustCreate(t, s, byCLI, b)
			c.advance(time.Second)
			mustUpdate(t, s, byEditor, setRent("1200")) // r2

			c.advance(tc.gap)
			mustUpdate(t, s, tc.origin, setRent("1300")) // r3

			if got, want := revisions(mustHistory(t, s)), []int64{3, 2, 1}; !slices.Equal(got, want) {
				t.Errorf("revisions %v, want %v", got, want)
			}
		})
	}
}

func TestHistoryIsCapped(t *testing.T) {
	s, b := newStore(t)
	s.HistoryLimit = 5
	mustCreate(t, s, byCLI, b)
	for i := range 9 {
		mustUpdate(t, s, byCLI, setRent(strconv.Itoa(i+1))) // r2–r10
	}

	entries := mustHistory(t, s)

	if got, want := revisions(entries), []int64{10, 9, 8, 7, 6}; !slices.Equal(got, want) {
		t.Errorf("revisions %v, want %v", got, want)
	}
	if entries[4].Undoable {
		t.Error("the oldest entry kept has no earlier version to go back to")
	}
	if lines := fileLines(t, s); lines > 2*s.HistoryLimit {
		t.Errorf("the file holds %d lines, more than twice the limit", lines)
	}
}

// A budget saved before history existed gets its current state recorded as
// the baseline on its first tracked write, so that write can be undone.
func TestBaselineForABudgetWithoutHistory(t *testing.T) {
	s, b := newStore(t)
	mustCreate(t, s, byCLI, b)
	if err := os.Remove(s.historyPath("home")); err != nil {
		t.Fatal(err)
	}

	mustUpdate(t, s, byAssistant, setRent("1200"))

	entries := mustHistory(t, s)
	if got, want := revisions(entries), []int64{2, 1}; !slices.Equal(got, want) {
		t.Fatalf("revisions %v, want %v", got, want)
	}
	if entries[1].Source != SourceEarlier {
		t.Errorf("baseline source: %s", entries[1].Source)
	}
	if rent := rentAt(t, s, 1); rent != "1150" {
		t.Errorf("rent in the baseline: %s", rent)
	}
}

// History is written before the budget. If the budget write then fails, the
// entry is newer than the budget: it must be ignored, and replaced by the
// next real write of that revision.
func TestEntryOfAFailedWriteIsIgnored(t *testing.T) {
	s, b := newStore(t)
	mustCreate(t, s, byCLI, b)
	current, err := s.Get(ctx, "home")
	if err != nil {
		t.Fatal(err)
	}
	failed := Record{ID: "home", Revision: 2, UpdatedAt: current.UpdatedAt, Budget: b}
	failed.Budget.Items = slices.Clone(b.Items)
	failed.Budget.Items[rentIndex(b)].Amount = money.MustDecimal("9999")
	if err := s.record(byAssistant, &current, failed); err != nil {
		t.Fatal(err)
	}

	listedBefore := revisions(mustHistory(t, s))
	mustUpdate(t, s, byCLI, setRent("1200"))

	if !slices.Equal(listedBefore, []int64{1}) {
		t.Errorf("the failed write is listed: %v", listedBefore)
	}
	if source := mustHistory(t, s)[0].Source; source != SourceCLI {
		t.Errorf("r2 source: %s", source)
	}
	if rent := rentAt(t, s, 2); rent != "1200" {
		t.Errorf("rent at r2 is from the failed write: %s", rent)
	}
}

func TestDeleteRemovesHistory(t *testing.T) {
	s, b := newStore(t)
	mustCreate(t, s, byCLI, b)

	if err := s.Delete(ctx, "home"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(s.historyPath("home")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("history left behind: %v", err)
	}
}

// A damaged history must never stop the budget from being saved: bad lines
// are skipped, and a line cut short by a crash doesn't swallow the next one.
func TestCorruptHistoryDoesNotBlockWrites(t *testing.T) {
	s, b := newStore(t)
	mustCreate(t, s, byCLI, b)
	garbage := "this is not json\n"
	halfWritten := `{"revision": 2, "budget": {"name"`
	if err := appendText(s.historyPath("home"), garbage+halfWritten); err != nil {
		t.Fatal(err)
	}

	mustUpdate(t, s, byAssistant, setRent("1200"))
	mustUpdate(t, s, byAssistant, setRent("1300"))

	if got, want := revisions(mustHistory(t, s)), []int64{3, 2, 1}; !slices.Equal(got, want) {
		t.Errorf("revisions %v, want %v", got, want)
	}
	if rent := rentAt(t, s, 2); rent != "1200" {
		t.Errorf("rent at r2: %s", rent)
	}
}

func appendText(path, text string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(text); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func fileLines(t testing.TB, s *FileStore) int {
	t.Helper()
	raw, err := os.ReadFile(s.historyPath("home"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(raw), "\n")
}

// BenchmarkAutosave is a save with a full history behind it: the cost of
// each keystroke the editor saves.
func BenchmarkAutosave(bm *testing.B) {
	s, b := newStore(bm)
	mustCreate(bm, s, byCLI, b)
	for i := range DefaultHistoryLimit {
		mustUpdate(bm, s, byCLI, setRent(strconv.Itoa(1000+i)))
	}
	bm.ResetTimer()
	for i := range bm.N {
		mustUpdate(bm, s, byCLI, setRent(strconv.Itoa(2000+i%1000)))
	}
}
