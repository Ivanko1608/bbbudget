package i18n_test

import (
	"io/fs"
	"strings"
	"testing"

	"bbbudget/internal/budget"
	"bbbudget/web"
)

func TestEveryRateSourceHasUILabel(t *testing.T) {
	catalog, err := fs.ReadFile(web.FS(), "i18n/en.json")
	if err != nil {
		t.Fatal(err)
	}

	for _, source := range budget.RateSources {
		key := "source_opt." + source
		if !strings.Contains(string(catalog), `"`+key+`":`) {
			t.Errorf("missing UI key %s", key)
		}
	}
}
