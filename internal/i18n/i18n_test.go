package i18n

import (
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"testing"

	"bbbudget/internal/money"
	"bbbudget/web"
)

var phRe = regexp.MustCompile(`\{([a-z_0-9]+)\}`)

func placeholders(forms map[string]string) []string {
	seen := map[string]bool{}
	for _, t := range forms {
		for _, m := range phRe.FindAllStringSubmatch(t, -1) {
			seen[m[1]] = true
		}
	}
	var out []string
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// checkParity holds every catalog to English: the same keys, the same
// placeholders, and plural forms for every category the locale's rule uses.
func checkParity(t *testing.T, where string, en map[string]map[string]string, loc string, rule string, cat map[string]map[string]string) {
	t.Helper()
	for k, ef := range en {
		f, ok := cat[k]
		if !ok {
			t.Errorf("%s %s: missing %s", where, loc, k)
			continue
		}
		if a, b := placeholders(ef), placeholders(f); !slices.Equal(a, b) {
			t.Errorf("%s %s: %s has placeholders %v, English has %v", where, loc, k, b, a)
		}
		_, plainEN := ef[""]
		_, plain := f[""]
		if plainEN != plain {
			t.Errorf("%s %s: %s is plural in one catalog only", where, loc, k)
		}
		if !plain {
			for _, c := range append(PluralCategories(rule), "other") {
				if _, ok := f[c]; !ok {
					t.Errorf("%s %s: %s lacks the %q form", where, loc, k, c)
				}
			}
		}
	}
	for k := range cat {
		if _, ok := en[k]; !ok {
			t.Errorf("%s %s: %s is not in English", where, loc, k)
		}
	}
}

func formsOf(c *Catalog) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, k := range c.Keys() {
		out[k], _ = c.Forms(k)
	}
	return out
}

func TestServerCatalogsMatchEnglish(t *testing.T) {
	cs := Catalogs()
	if len(cs) < 2 {
		t.Fatalf("expected English plus at least one translation, have %v", Locales())
	}
	en := formsOf(cs[Default])
	for loc, c := range cs {
		checkParity(t, "server", en, loc, c.Meta.Plural, formsOf(c))
	}
}

// The browser catalogs follow the same rules. Their locales must match the
// server's, so the UI never shows a language the insights can't speak.
func TestUICatalogsMatchEnglish(t *testing.T) {
	load := func(loc string) map[string]map[string]string {
		raw, err := fsRead("i18n/" + loc + ".json")
		if err != nil {
			t.Fatalf("UI catalog for %s: %v", loc, err)
		}
		var all map[string]json.RawMessage
		if err := json.Unmarshal(raw, &all); err != nil {
			t.Fatalf("UI catalog %s: %v", loc, err)
		}
		out := map[string]map[string]string{}
		for k, v := range all {
			var s string
			if json.Unmarshal(v, &s) == nil {
				out[k] = map[string]string{"": s}
				continue
			}
			var f map[string]string
			if err := json.Unmarshal(v, &f); err != nil {
				t.Fatalf("UI catalog %s: %s: %v", loc, k, err)
			}
			out[k] = f
		}
		return out
	}
	en := load(Default)
	for loc, c := range Catalogs() {
		checkParity(t, "ui", en, loc, c.Meta.Plural, load(loc))
	}
}

func fsRead(name string) ([]byte, error) {
	f, err := web.FS().Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	st, _ := f.Stat()
	buf := make([]byte, st.Size())
	_, err = f.Read(buf)
	return buf, err
}

func TestPluralRules(t *testing.T) {
	ru := map[int64]string{0: "many", 1: "one", 2: "few", 4: "few", 5: "many", 11: "many", 12: "many", 14: "many", 21: "one", 22: "few", 25: "many", 101: "one", 111: "many"}
	for n, want := range ru {
		if got := pluralRules["east_slavic"].pick(n); got != want {
			t.Errorf("east_slavic(%d) = %s, want %s", n, got, want)
		}
	}
	if pluralRules["one_other"].pick(1) != "one" || pluralRules["one_other"].pick(0) != "other" {
		t.Error("one_other")
	}
	// Each rule lists exactly the categories it can pick.
	for name, r := range pluralRules {
		seen := map[string]bool{}
		for n := int64(0); n < 200; n++ {
			seen[r.pick(n)] = true
		}
		got := make([]string, 0, len(seen))
		for c := range seen {
			got = append(got, c)
		}
		sort.Strings(got)
		if !slices.Equal(got, r.categories) {
			t.Errorf("%s picks %v but lists %v", name, got, r.categories)
		}
	}
}

func TestSigns(t *testing.T) {
	eur := money.MustLookup("EUR")
	neg, _ := money.FromDecimal(money.MustDecimal("-1234.5"), eur)
	zero, _ := money.FromDecimal(money.MustDecimal("-0.4"), eur)
	cases := []struct{ got, want string }{
		{For("en").Money(neg), "−€1,234"},
		{For("ru").Money(neg), "−1\u00a0234\u00a0€"},
		{For("en").MoneyExact(neg), "−€1,234.50"},
		{For("en").Money(zero), "€0"},
		{For("en").Number("-1234.5"), "−1,234.5"},
		{For("en").Arg(Int(-1500)), "−1,500"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

func TestNestedArg(t *testing.T) {
	m := Msg{Key: "change.added", Args: map[string]Arg{"part": Nested(Msg{Key: "part.item"}), "name": Text("Gym")}}
	if got := For("en").Render(m); got != "added item Gym" {
		t.Errorf("nested: %q", got)
	}
	if !For("ru").Has("part.item") || For("ru").Has("no.such.key") {
		t.Error("Has")
	}
}

func TestRenderRussian(t *testing.T) {
	l := For("ru-RU")
	cases := map[int64]string{1: "каждый 1 месяц", 2: "каждые 2 месяца", 5: "каждые 5 месяцев", 21: "каждый 21 месяц"}
	for n, want := range cases {
		if got := l.Render(Msg{Key: "schedule.every", Args: map[string]Arg{"count": Int(n)}}); got != want {
			t.Errorf("schedule.every %d: %q, want %q", n, got, want)
		}
	}
	eur := money.MustLookup("EUR")
	m, _ := money.FromDecimal(money.MustDecimal("1234.50"), eur)
	if got := l.Money(m); got != "1 234 €" {
		t.Errorf("ru money: %q", got)
	}
	if got := l.MoneyExact(m); got != "1 234,50 €" {
		t.Errorf("ru exact money: %q", got)
	}
	if got := For("en").Money(m); got != "€1,234" { // half-even: 1234.50 → 1234
		t.Errorf("en money: %q", got)
	}
	btc, _ := money.FromDecimal(money.MustDecimal("0.015"), money.MustLookup("BTC"))
	if got := l.Money(btc); got != "0,015 BTC" {
		t.Errorf("ru crypto: %q", got)
	}
	if got := l.Month("2026-10"); got != "окт. 2026" {
		t.Errorf("ru month: %q", got)
	}
}

func TestFallbacks(t *testing.T) {
	if l := For("xx-YY"); l.Locale() != Default {
		t.Errorf("unknown locale: %s", l.Locale())
	}
	if got := For("ru").Render(Msg{Key: "no.such.key"}); got != "no.such.key" {
		t.Errorf("missing key: %q", got)
	}
	for tag, want := range map[string]string{"ru_RU.UTF-8": "ru", "RU": "ru", "en-GB": "en", "de": Default, "": Default} {
		if got := Match(tag); got != want {
			t.Errorf("Match(%q) = %s, want %s", tag, got, want)
		}
	}
}

func TestAcceptLanguage(t *testing.T) {
	cases := map[string]string{
		"ru-RU,ru;q=0.9,en;q=0.8": "ru",
		"de-DE,de;q=0.9,ru;q=0.5": "ru", // first language we have
		"en-US,en;q=0.9,ru;q=0.8": "en",
		"ru;q=0.2,en;q=0.9":       "en", // q wins over order
		"de, fr":                  "",
		"ru;q=0":                  "",
		"ru;q=abc":                "",
		"":                        "",
	}
	for h, want := range cases {
		if got := MatchAcceptLanguage(h); got != want {
			t.Errorf("MatchAcceptLanguage(%q) = %q, want %q", h, got, want)
		}
	}
}
