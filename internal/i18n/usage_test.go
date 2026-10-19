package i18n

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"bbbudget/web"
)

// Every key the Go code names must be in the English server catalog.
// Keys built at run time ("month.short."+m) are checked by family below.
func TestGoKeysExist(t *testing.T) {
	keyRe := regexp.MustCompile(`"((?:insight|issue|schedule|change|part|compare|cli|category|month)\.[a-z0-9_.-]*[a-z0-9_-])"`)
	en := Catalogs()[Default]
	err := filepath.WalkDir("../..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range keyRe.FindAllStringSubmatch(string(src), -1) {
			if _, ok := en.Forms(m[1]); !ok {
				t.Errorf("%s uses %q, which is not in the English catalog", path, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for m := 1; m <= 12; m++ {
		if _, ok := en.Forms("month.short." + strconv.Itoa(m)); !ok {
			t.Errorf("missing month.short.%d", m)
		}
	}
	for _, k := range []string{"currency", "category", "item", "debt", "pot", "withdrawal", "scenario"} {
		if _, ok := en.Forms("part." + k); !ok {
			t.Errorf("missing part.%s", k)
		}
	}
}

// Every key the UI names (t("…"), data-i18n, data-i18n-attr) must be in the
// English UI catalog, and so must every member of the families it builds.
func TestUIKeysExist(t *testing.T) {
	raw, err := fsRead("i18n/en.json")
	if err != nil {
		t.Fatal(err)
	}
	have := string(raw)
	exists := func(k string) bool { return strings.Contains(have, `"`+k+`":`) }
	// Any string literal in a script that starts like a catalog key is one,
	// which also catches keys picked by a condition: t(ok ? "a.b" : "a.c").
	prefixes := map[string]bool{}
	for _, k := range regexp.MustCompile(`"([a-z_]+)\.[a-z0-9_.-]+":`).FindAllStringSubmatch(have, -1) {
		prefixes[k[1]] = true
	}
	literal := regexp.MustCompile(`"([a-z_]+)\.([a-z0-9_.-]*[a-z0-9_])"`)
	pats := []*regexp.Regexp{
		regexp.MustCompile(`\bt(?:HTML)?\("([a-z0-9_.-]+)"`),
		regexp.MustCompile(`data-i18n="([a-z0-9_.-]+)"`),
		regexp.MustCompile(`[:;]\s*([a-z]+\.[a-z0-9_.-]+)`), // inside data-i18n-attr="attr:key;…"
	}
	err = fs.WalkDir(web.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !(strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".html")) {
			return err
		}
		src, err := fs.ReadFile(web.FS(), path)
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, ".js") {
			for _, m := range literal.FindAllStringSubmatch(string(src), -1) {
				if key := m[1] + "." + m[2]; prefixes[m[1]] && !exists(key) {
					t.Errorf("%s uses %q, which is not in the English UI catalog", path, key)
				}
			}
		}
		for i, re := range pats {
			text := string(src)
			if i == 2 { // only look inside data-i18n-attr values
				var attrs []string
				for _, m := range regexp.MustCompile(`data-i18n-attr="([^"]+)"`).FindAllStringSubmatch(text, -1) {
					attrs = append(attrs, ";"+m[1])
				}
				text = strings.Join(attrs, "")
			}
			for _, m := range re.FindAllStringSubmatch(text, -1) {
				if !exists(m[1]) {
					t.Errorf("%s uses %q, which is not in the English UI catalog", path, m[1])
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	families := map[string][]string{
		"source_opt.":         {"manual", "auto", "ecb", "currency-api", "coingecko"},
		"every.":              {"0", "1", "2", "3", "6", "12"},
		"sched.every.":        {"1", "3", "12"},
		"strategy.":           {"avalanche", "snowball", "listed"},
		"scenarios.kind.":     {"item", "rate", "extra", "remove"},
		"source.":             {"web", "mcp", "cli", "auto-refresh", "earlier", "unknown"},
		"prefs.files.":        {"data", "templates", "settings"},
		"pot_kind.":           {"general", "sinking", "emergency"},
		"pot.help.":           {"general", "sinking", "emergency"},
		"short.note.":         {"sinking", "emergency"},
		"table.":              {"month", "income", "spent", "to_debts", "left", "from_savings", "owed", "saved", "savings", "debt"},
		"compare.":            {"scenario", "this_month", "end_savings", "delta", "interest", "goals"},
		"month_status.state.": {"over", "projected_over", "unplanned", "short"},
	}
	for prefix, members := range families {
		for _, m := range members {
			if !exists(prefix + m) {
				t.Errorf("missing UI key %s%s", prefix, m)
			}
		}
	}
}
