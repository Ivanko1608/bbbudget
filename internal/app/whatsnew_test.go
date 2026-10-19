package app

import (
	"testing"

	"bbbudget/internal/store"
)

func TestFindOwnLastChange(t *testing.T) {
	newestFirst := []store.Entry{
		{Revision: 5, Source: store.SourceWeb},
		{Revision: 4, Source: store.SourceMCP},
		{Revision: 3, Source: store.SourceWeb},
	}
	tests := map[string]struct {
		history      []store.Entry
		wantRevision int64
		wantFound    bool
	}{
		"asker's newest change": {newestFirst, 4, true},
		"no change by the asker": {
			newestFirst[:1], 5, true,
		},
		"empty history": {nil, 0, false},
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
