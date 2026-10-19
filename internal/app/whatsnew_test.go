package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"bbbudget/internal/store"
)

func TestFindOwnLastChange(t *testing.T) {
	tests := map[string]struct {
		history      []store.Entry
		wantRevision int64
		wantFound    bool
	}{
		"asker's newest change": {
			history: []store.Entry{
				{Revision: 5, Source: store.SourceWeb},
				{Revision: 4, Source: store.SourceMCP},
				{Revision: 3, Source: store.SourceMCP},
			},
			wantRevision: 4,
			wantFound:    true,
		},
		"no change by the asker keeps the oldest": {
			history: []store.Entry{
				{Revision: 5, Source: store.SourceWeb},
				{Revision: 3, Source: store.SourceWeb},
			},
			wantRevision: 3,
			wantFound:    true,
		},
		"empty history": {
			history:      nil,
			wantRevision: 0,
			wantFound:    false,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			revision, found := findOwnLastChange(test.history, store.SourceMCP)

			if revision != test.wantRevision {
				t.Errorf("revision: got %d, want %d", revision, test.wantRevision)
			}
			if found != test.wantFound {
				t.Errorf("found: got %v, want %v", found, test.wantFound)
			}
		})
	}
}

// storeWithoutHistory is a store whose budgets have no history entries.
type storeWithoutHistory struct {
	store.Store
}

func (storeWithoutHistory) History(context.Context, string, int) ([]store.Entry, error) {
	return nil, nil
}

func TestWhatsNewFromOwnLastChangeNeedsHistory(t *testing.T) {
	a := testApp(t)
	a.Store = storeWithoutHistory{Store: a.Store}
	request := WhatsNewRequest{Asker: store.SourceMCP, FromRevision: FromOwnLastChange}

	_, err := a.WhatsNew(context.Background(), "home", request)

	if !errors.Is(err, ErrBadInput) {
		t.Fatalf("error: got %v, want ErrBadInput", err)
	}
	if !strings.Contains(err.Error(), "the budget has no history") {
		t.Errorf("error: %v", err)
	}
}
