// Package i18n renders user-facing text in the reader's language.
//
// The domain never builds sentences: it emits a Msg (a catalog key plus typed
// arguments) and the edges (HTTP, MCP, CLI) render it with a Localizer. Each
// locale is one JSON catalog in locales/, holding its messages and how it
// writes numbers, so adding a language is adding a file. Money is formatted
// from exact values; there are no floats here either.
package i18n

import (
	"cmp"
	"embed"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"bbbudget/internal/money"
)

// Default is the fallback locale; every key must exist in it.
const Default = "en"

// Msg is a message to render: a catalog key and its arguments.
type Msg struct {
	Key  string         `json:"key"`
	Args map[string]Arg `json:"args,omitempty"`
}

// Arg is one typed message argument. Exactly one field is set. Typed
// arguments let each locale format them its own way, and give assistants the
// exact values rather than prose to re-parse.
type Arg struct {
	Money *money.Money `json:"money,omitempty"`
	// Month is "YYYY-MM", rendered as the locale's short month and year.
	Month string `json:"month,omitempty"`
	// Int is a whole number; a "count" argument also picks the plural form.
	Int *int64 `json:"int,omitempty"`
	// Number is an exact decimal (an exchange rate), shown with the locale's separators.
	Number string `json:"number,omitempty"`
	// Text is shown as is: names the user typed, currency codes.
	Text string `json:"text,omitempty"`
	// Msg is another message, rendered in place ("added {kind} {name}",
	// where kind is the message "item").
	Msg *Msg `json:"msg,omitempty"`
	// List is rendered item by item and joined with the locale's list
	// separator, or with Sep when set.
	List []Msg  `json:"list,omitempty"`
	Sep  string `json:"sep,omitempty"`
}

// Helpers for building arguments.
func Money(m money.Money) Arg { return Arg{Money: &m} }
func Month(ym string) Arg     { return Arg{Month: ym} }
func Int(n int64) Arg         { return Arg{Int: &n} }
func Num(d string) Arg        { return Arg{Number: d} }
func Text(s string) Arg       { return Arg{Text: s} }
func Nested(m Msg) Arg        { return Arg{Msg: &m} }
func List(ms []Msg) Arg       { return Arg{List: ms} }

// JoinedList is a list joined with sep in every language ("; " between changes).
func JoinedList(ms []Msg, sep string) Arg { return Arg{List: ms, Sep: sep} }

// Meta is how a locale writes things, from the "@meta" entry of its catalog.
type Meta struct {
	// Name is the language's own name ("Deutsch").
	Name string `json:"name"`
	// Group and Decimal are the digit-group and decimal separators.
	Group   string `json:"group"`
	Decimal string `json:"decimal"`
	// Currency is "symbol" to keep each currency's own placement (€12 but
	// 12 CHF), or "after" to always write 12 €.
	Currency string `json:"currency"`
	// CurrencySep goes between the amount and a symbol written after it.
	CurrencySep string `json:"currency_sep"`
	// ListSep joins list items.
	ListSep string `json:"list_sep"`
	// Plural names the plural rule (see pluralRules).
	Plural string `json:"plural"`
}

// Catalog is one locale's messages. A message is a string, or an object of
// plural forms ("one", "few", "many", "other") chosen by the "count" argument.
type Catalog struct {
	Locale   string
	Meta     Meta
	messages map[string]message
}

type message struct {
	text  string
	forms map[string]string
}

func (m *message) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &m.text); err == nil {
		return nil
	}
	return json.Unmarshal(b, &m.forms)
}

//go:embed locales/*.json
var localeFS embed.FS

var catalogs = sync.OnceValues(loadCatalogs)

func loadCatalogs() (map[string]*Catalog, error) {
	files, err := localeFS.ReadDir("locales")
	if err != nil {
		return nil, err
	}
	out := map[string]*Catalog{}
	for _, f := range files {
		raw, err := localeFS.ReadFile("locales/" + f.Name())
		if err != nil {
			return nil, err
		}
		c, err := parseCatalog(strings.TrimSuffix(f.Name(), path.Ext(f.Name())), raw)
		if err != nil {
			return nil, err
		}
		out[c.Locale] = c
	}
	return out, nil
}

func parseCatalog(locale string, raw []byte) (*Catalog, error) {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, fmt.Errorf("locale %s: %w", locale, err)
	}
	c := &Catalog{Locale: locale, messages: map[string]message{}}
	if err := json.Unmarshal(all["@meta"], &c.Meta); err != nil {
		return nil, fmt.Errorf("locale %s: @meta: %w", locale, err)
	}
	if _, ok := pluralRules[c.Meta.Plural]; !ok {
		return nil, fmt.Errorf("locale %s: unknown plural rule %q", locale, c.Meta.Plural)
	}
	delete(all, "@meta")
	for k, v := range all {
		var m message
		if err := json.Unmarshal(v, &m); err != nil {
			return nil, fmt.Errorf("locale %s: %s: %w", locale, k, err)
		}
		c.messages[k] = m
	}
	return c, nil
}

// Catalogs returns every built-in catalog by locale.
func Catalogs() map[string]*Catalog {
	cs, err := catalogs()
	if err != nil {
		panic("i18n: " + err.Error()) // embedded files; caught by the tests
	}
	return cs
}

// Locales lists the available locales, sorted.
func Locales() []string {
	return slices.Sorted(maps.Keys(Catalogs()))
}

// Keys lists a catalog's message keys, sorted.
func (c *Catalog) Keys() []string {
	return slices.Sorted(maps.Keys(c.messages))
}

// Has reports whether the catalog has a message for key.
func (c *Catalog) Has(key string) bool {
	_, ok := c.messages[key]
	return ok
}

// Forms returns a message's text, or its plural forms by category.
func (c *Catalog) Forms(key string) (map[string]string, bool) {
	m, ok := c.messages[key]
	if !ok {
		return nil, false
	}
	if m.forms != nil {
		return m.forms, true
	}
	return map[string]string{"": m.text}, true
}

// Localizer renders messages in one locale, falling back to English for a
// key the locale lacks.
type Localizer struct {
	cat, fallback *Catalog
}

// For returns a Localizer for the best match of tag ("de", "de-AT", "en-GB");
// unknown tags get English.
func For(tag string) *Localizer {
	cs := Catalogs()
	return &Localizer{cat: cs[Match(tag)], fallback: cs[Default]}
}

// Locale is the locale actually used.
func (l *Localizer) Locale() string { return l.cat.Locale }

// Meta is the locale's formatting data.
func (l *Localizer) Meta() Meta { return l.cat.Meta }

// Has reports whether the locale, or the English fallback, has a message for key.
func (l *Localizer) Has(key string) bool { return l.cat.Has(key) || l.fallback.Has(key) }

// Match returns the available locale for a language tag, or Default.
func Match(tag string) string {
	if m, ok := supported(tag); ok {
		return m
	}
	return Default
}

// supported returns the available locale a language tag names, English
// included, and whether there is one.
func supported(tag string) (string, bool) {
	tag = strings.ToLower(strings.TrimSpace(tag))
	tag = strings.ReplaceAll(tag, "_", "-")
	if i := strings.IndexAny(tag, ".@"); i >= 0 { // POSIX: de_DE.UTF-8
		tag = tag[:i]
	}
	cs := Catalogs()
	if _, ok := cs[tag]; ok {
		return tag, true
	}
	if base, _, _ := strings.Cut(tag, "-"); base != "" {
		if _, ok := cs[base]; ok {
			return base, true
		}
	}
	return "", false
}

// MatchAcceptLanguage picks the best available locale from an HTTP
// Accept-Language header, honouring q-values; "" if none matches.
func MatchAcceptLanguage(header string) string {
	type cand struct {
		tag string
		q   int // thousandths
	}
	var cands []cand
	for _, part := range strings.Split(header, ",") {
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		q := 1000
		if v, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			q = parseQ(v)
		}
		if tag != "" && tag != "*" && q > 0 {
			cands = append(cands, cand{tag, q})
		}
	}
	slices.SortStableFunc(cands, func(a, b cand) int { return cmp.Compare(b.q, a.q) })
	for _, c := range cands {
		if m, ok := supported(c.tag); ok {
			return m
		}
	}
	return ""
}

// parseQ reads an HTTP quality value ("0.8", "1", "0.125") as thousandths;
// anything malformed is 0, which drops the language.
func parseQ(v string) int {
	whole, frac, _ := strings.Cut(strings.TrimSpace(v), ".")
	if (whole != "0" && whole != "1") || len(frac) > 3 || strings.Trim(frac, "0123456789") != "" {
		return 0
	}
	thousandths, _ := strconv.Atoi(frac + strings.Repeat("0", 3-len(frac)))
	if whole == "1" {
		thousandths += 1000
	}
	return min(thousandths, 1000)
}

// FromEnv picks a locale from LC_ALL, LC_MESSAGES or LANG, or Default.
func FromEnv() string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(k); v != "" && v != "C" && v != "POSIX" {
			return Match(v)
		}
	}
	return Default
}

var placeholder = regexp.MustCompile(`\{([a-z_0-9]+)\}`)

// Render renders a message. Missing keys render as the key itself, so a gap
// is visible rather than blank (the catalog tests keep that from shipping).
func (l *Localizer) Render(m Msg) string {
	cat := l.cat
	forms, ok := cat.Forms(m.Key)
	if !ok {
		cat = l.fallback
		forms, ok = cat.Forms(m.Key)
	}
	if !ok {
		return m.Key
	}
	text, ok := forms[""]
	if !ok {
		text = pluralText(cat, forms, m.Args["count"])
	}
	return placeholder.ReplaceAllStringFunc(text, func(ph string) string {
		a, ok := m.Args[ph[1:len(ph)-1]]
		if !ok {
			return ph
		}
		return l.Arg(a)
	})
}

// pluralText picks the form for count with the rule of the catalog the
// message came from (a fallback's English forms need English rules).
func pluralText(cat *Catalog, forms map[string]string, count Arg) string {
	var n int64
	if count.Int != nil {
		n = *count.Int
	}
	if t, ok := forms[pluralRules[cat.Meta.Plural].pick(n)]; ok {
		return t
	}
	return forms["other"]
}

// Arg renders one argument.
func (l *Localizer) Arg(a Arg) string {
	switch {
	case a.Money != nil:
		return l.Money(*a.Money)
	case a.Month != "":
		return l.Month(a.Month)
	case a.Int != nil:
		return l.Number(fmt.Sprint(*a.Int))
	case a.Number != "":
		return l.Number(a.Number)
	case a.Msg != nil:
		return l.Render(*a.Msg)
	case a.List != nil:
		parts := make([]string, len(a.List))
		for i, m := range a.List {
			parts[i] = l.Render(m)
		}
		if a.Sep != "" {
			return strings.Join(parts, a.Sep)
		}
		return strings.Join(parts, l.cat.Meta.ListSep)
	}
	return a.Text
}

// Month renders "YYYY-MM" as the locale's short month and year ("Jan 2026").
func (l *Localizer) Month(ym string) string {
	y, m, ok := strings.Cut(ym, "-")
	if !ok {
		return ym
	}
	return l.Render(Msg{Key: "month.short." + strings.TrimLeft(m, "0"), Args: map[string]Arg{"year": Text(y)}})
}

// Number renders an exact decimal string with the locale's separators.
func (l *Localizer) Number(d string) string {
	sign, digits := splitSign(d)
	return sign + l.unsigned(digits)
}

// Money renders an amount the way money.Money.Display does (whole units for
// fiat, the exact amount without trailing zeros for crypto) in this locale.
func (l *Localizer) Money(m money.Money) string {
	if m.Currency().Crypto {
		return l.withSymbol(m.Currency(), m.Decimal().String())
	}
	return l.withSymbol(m.Currency(), money.RoundInt(m.Rat(), money.HalfEven).String())
}

// MoneyExact renders an amount with all its minor digits ("€1,234.50").
func (l *Localizer) MoneyExact(m money.Money) string {
	return l.withSymbol(m.Currency(), m.String())
}

// withSymbol renders a decimal amount with the currency symbol where the
// locale puts it, and the sign before both ("−€12", "−12 €").
func (l *Localizer) withSymbol(c money.Currency, d string) string {
	sign, digits := splitSign(d)
	num := l.unsigned(digits)
	if c.SymbolFirst && l.cat.Meta.Currency != "after" {
		return sign + c.Symbol + num
	}
	return sign + num + l.cat.Meta.CurrencySep + c.Symbol
}

// splitSign separates a decimal's sign from its digits. The sign is a real
// minus ("−"), and a negative zero has none.
func splitSign(d string) (sign, digits string) {
	digits = strings.TrimLeft(d, "+-")
	if strings.HasPrefix(d, "-") && strings.Trim(digits, "0.") != "" {
		sign = "−"
	}
	return sign, digits
}

// unsigned renders digits without a sign: groups, then the decimal part.
func (l *Localizer) unsigned(digits string) string {
	intPart, frac, _ := strings.Cut(digits, ".")
	var b strings.Builder
	for i, ch := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteString(l.cat.Meta.Group)
		}
		b.WriteRune(ch)
	}
	if frac != "" {
		b.WriteString(l.cat.Meta.Decimal + frac)
	}
	return b.String()
}

// pluralRule maps a whole number to its CLDR plural category.
type pluralRule struct {
	// categories are every category pick can return, sorted.
	categories []string
	pick       func(n int64) string
}

var pluralRules = map[string]pluralRule{
	// English, German, Spanish, Italian, Dutch, Swedish, ...
	"one_other": {[]string{"one", "other"}, func(n int64) string {
		if n == 1 {
			return "one"
		}
		return "other"
	}},
	// French, Portuguese (Brazil): 0 and 1 are "one".
	"french": {[]string{"one", "other"}, func(n int64) string {
		if n == 0 || n == 1 {
			return "one"
		}
		return "other"
	}},
	// Russian and the other East Slavic languages.
	"east_slavic": {[]string{"few", "many", "one"}, func(n int64) string {
		n = abs(n)
		switch m10, m100 := n%10, n%100; {
		case m10 == 1 && m100 != 11:
			return "one"
		case m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14):
			return "few"
		default:
			return "many"
		}
	}},
	// Polish.
	"polish": {[]string{"few", "many", "one"}, func(n int64) string {
		n = abs(n)
		switch m10, m100 := n%10, n%100; {
		case n == 1:
			return "one"
		case m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14):
			return "few"
		default:
			return "many"
		}
	}},
	// Chinese, Japanese, Korean, ...: no plural forms.
	"none": {[]string{"other"}, func(int64) string { return "other" }},
}

// PluralCategories lists the categories a rule can produce; each catalog
// message with plural forms must cover them.
func PluralCategories(rule string) []string { return pluralRules[rule].categories }

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// Pseudo returns a Localizer whose every message is English wrapped in
// ⟦…⟧. Tests render with it: text without the brackets came from somewhere
// other than the catalog, so it can't be translated.
func Pseudo() *Localizer {
	en := Catalogs()[Default]
	c := &Catalog{Locale: "en-XA", Meta: en.Meta, messages: map[string]message{}}
	for k, m := range en.messages {
		w := message{text: "⟦" + m.text + "⟧"}
		if m.forms != nil {
			w = message{forms: map[string]string{}}
			for f, t := range m.forms {
				w.forms[f] = "⟦" + t + "⟧"
			}
		}
		c.messages[k] = w
	}
	return &Localizer{cat: c, fallback: c}
}

// StripMdBold removes the bold markers that catalog messages carry.
func StripMdBold(s string) string { return strings.ReplaceAll(s, "**", "") }
