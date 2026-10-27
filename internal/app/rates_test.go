package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"bbbudget/internal/budget"
	"bbbudget/internal/i18n"
	"bbbudget/internal/money"
	"bbbudget/internal/rates"
	"bbbudget/internal/settings"
	"bbbudget/internal/store"
)

// byTest is the origin of the writes tests make.
var (
	byTest  = store.Origin{Source: store.SourceCLI}
	english = i18n.For(i18n.Default)
)

// testApp holds the example budget ("home": EUR base, CHF and USD typed by
// hand) and offline rate providers: "ecb" knows USD and CHF, "currency-api"
// also knows BTC, and "coingecko" is down.
func testApp(t *testing.T) *App {
	t.Helper()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := New(st, nil)
	a.Now = func() time.Time { return time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC) }
	ecb := rates.Fixed{
		ID:    budget.RateSourceECB,
		Base:  "EUR",
		Date:  "2026-10-07",
		Rates: map[string]string{"USD": "1.1186", "CHF": "0.9326"},
	}
	capi := rates.Fixed{
		ID:    budget.RateSourceCurrencyAPI,
		Base:  "EUR",
		Date:  "2026-10-08",
		Rates: map[string]string{"USD": "1.12", "BTC": "1.35211580123e-05"},
	}
	a.Rates = map[budget.RateSource]rates.Provider{
		budget.RateSourceECB: ecb, budget.RateSourceCurrencyAPI: capi,
		budget.RateSourceCoinGecko: rates.Fixed{
			ID:  budget.RateSourceCoinGecko,
			Err: errors.New("HTTP 429"),
		},
		budget.RateSourceAuto: rates.Chain{
			ID:        budget.RateSourceAuto,
			Providers: []rates.Provider{ecb, capi},
		},
	}
	ctx := context.Background()
	if _, err := a.Create(ctx, byTest, english, "home", "Home", "example"); err != nil {
		t.Fatal(err)
	}
	withSettings(t, a, settings.Patch{FetchRates: ptr(true)}) // off by default; these tests fetch
	return a
}

func eq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

func setCurrency(code string, patch budget.CurrencyPatch) []budget.Op {
	return []budget.Op{budget.NewOp("set_currency", code, patch)}
}

func currency(t *testing.T, a *App, code string) budget.CurrencyRate {
	t.Helper()
	v, err := a.Get(context.Background(), "home")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range v.Budget.Currencies {
		if c.Code == code {
			return c
		}
	}
	t.Fatalf("no currency %s", code)
	return budget.CurrencyRate{}
}

func TestRegistryCoversEverySource(t *testing.T) {
	reg := rates.Registry(nil, nil)
	for _, s := range budget.RateSources {
		if _, ok := reg[s]; !ok && s != budget.RateSourceManual {
			t.Errorf("source %q has no provider", s)
		}
	}
}

func TestRefreshSkipsManualUnlessNamed(t *testing.T) {
	a, ctx := testApp(t), context.Background()
	v, updates, err := a.RefreshRates(ctx, byTest, "home", nil)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "updates", len(updates), 0)
	eq(t, "USD per base", currency(t, a, "USD").PerBase.String(), "1.08")
	rev := v.Revision

	v, updates, err = a.RefreshRates(ctx, byTest, "home", []string{"usd"})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "updates", len(updates), 1)
	eq(t, "old rate", updates[0].Old.String(), "1.08")
	eq(t, "new rate", updates[0].New.String(), "1.1186")
	eq(t, "update provider", updates[0].From, budget.RateSourceECB)
	usd := currency(t, a, "USD")
	eq(t, "USD source", usd.Source, budget.RateSourceAuto)
	eq(t, "USD rate date", usd.RateDate, "2026-10-07")
	eq(t, "USD rate provider", usd.RateFrom, budget.RateSourceECB)
	eq(t, "USD per base", usd.PerBase.String(), "1.1186")
	eq(t, "revision after refresh", v.Revision, rev+1)
	eq(t, "CHF per base", currency(t, a, "CHF").PerBase.String(), "0.94")

	// From now on a plain refresh includes USD.
	_, updates, _ = a.RefreshRates(ctx, byTest, "home", nil)
	eq(t, "updates", len(updates), 1)
	eq(t, "updated code", updates[0].Code, "USD")
	if _, _, err := a.RefreshRates(ctx, byTest, "home", []string{"JPY"}); err == nil {
		t.Fatal("refreshing a currency the budget doesn't have")
	}
}

func TestSetCurrencyFetchesCryptoRate(t *testing.T) {
	a, ctx := testApp(t), context.Background()
	if _, err := a.SetCurrency(ctx, byTest, "home", "btc", budget.CurrencyPatch{}); err != nil {
		t.Fatal(err)
	}
	btc := currency(t, a, "BTC")
	// ECB has no BTC, so auto falls back; the rate keeps 10 significant digits.
	eq(t, "BTC per base", btc.PerBase.String(), "0.00001352115801")
	eq(t, "BTC source", btc.Source, budget.RateSourceAuto)
	eq(t, "BTC rate provider", btc.RateFrom, budget.RateSourceCurrencyAPI)

	// No provider prices it: a clear error, nothing saved.
	_, err := a.SetCurrency(ctx, byTest, "home", "ETH", budget.CurrencyPatch{})
	var re *RateError
	if !errors.As(err, &re) {
		t.Fatalf("want RateError, got %v", err)
	}
	// Unless the rate is given.
	rate := money.MustDecimal("0.0004")
	typed := budget.CurrencyPatch{PerBase: &rate}
	if _, err := a.SetCurrency(ctx, byTest, "home", "ETH", typed); err != nil {
		t.Fatal(err)
	}
	if source := currency(t, a, "ETH").Source; source != budget.RateSourceManual {
		t.Fatalf("ETH source = %q, want %q", source, budget.RateSourceManual)
	}
}

func TestPartialAndTotalFailure(t *testing.T) {
	a, ctx := testApp(t), context.Background()
	gecko := budget.RateSourceCoinGecko
	if _, err := a.SetCurrency(ctx, byTest, "home", "BTC", budget.CurrencyPatch{}); err != nil {
		t.Fatal(err)
	}
	toGecko := setCurrency("BTC", budget.CurrencyPatch{Source: &gecko})
	if _, err := a.Apply(ctx, byTest, "home", store.AnyRevision, toGecko); err != nil {
		t.Fatal(err)
	}
	before := currency(t, a, "BTC").PerBase.String()

	_, updates, err := a.RefreshRates(ctx, byTest, "home", []string{"USD", "BTC"})
	if err != nil {
		t.Fatalf("one currency failing must not fail the rest: %v", err)
	}
	i := slices.IndexFunc(updates, func(u RateUpdate) bool { return u.Code == "BTC" })
	if updates[i].Error == "" {
		t.Fatalf("BTC update has no error: %+v", updates)
	}
	eq(t, "BTC per base", currency(t, a, "BTC").PerBase.String(), before)
	eq(t, "USD per base", currency(t, a, "USD").PerBase.String(), "1.1186")

	v, _ := a.Get(ctx, "home")
	_, _, err = a.RefreshRates(ctx, byTest, "home", []string{"BTC"})
	var re *RateError
	if !errors.As(err, &re) {
		t.Fatalf("want RateError, got %v", err)
	}
	if after, _ := a.Get(ctx, "home"); after.Revision != v.Revision {
		t.Fatal("a failed refresh must not write")
	}
}

func TestQuoteRejectsBadInput(t *testing.T) {
	a := testApp(t)
	badQuotes := []struct {
		base   string
		source budget.RateSource
		code   string
	}{
		{"EUR", "google", "USD"},
		{"EUR", "", "XXX"},
		{"ZZZ", "", "USD"},
	}
	for _, c := range badQuotes {
		_, err := a.Quote(context.Background(), c.base, c.source, []string{c.code})
		if !errors.Is(err, ErrBadInput) {
			t.Errorf("%v: want ErrBadInput, got %v", c, err)
		}
	}
}

// hook is a provider that runs a function mid-fetch, like a user editing the
// budget while a slow request is in flight.
type hook struct {
	rates.Provider
	during func()
}

func (h hook) Quotes(
	ctx context.Context,
	base string,
	codes []string,
) (map[string]rates.Quote, error) {
	h.during()
	return h.Provider.Quotes(ctx, base, codes)
}

func TestRefreshKeepsRateTypedDuringFetch(t *testing.T) {
	a, ctx := testApp(t), context.Background()
	bothAuto := []string{"USD", "CHF"}
	if _, _, err := a.RefreshRates(ctx, byTest, "home", bothAuto); err != nil {
		t.Fatal(err)
	}
	typed := money.MustDecimal("1.05")
	a.Rates[budget.RateSourceAuto] = hook{a.Rates[budget.RateSourceAuto], func() {
		typeRate := setCurrency("USD", budget.CurrencyPatch{PerBase: &typed})
		if _, err := a.Apply(ctx, byTest, "home", store.AnyRevision, typeRate); err != nil {
			t.Error(err)
		}
	}}
	_, updates, err := a.RefreshRates(ctx, byTest, "home", nil)
	if err != nil {
		t.Fatal(err)
	}
	usd := currency(t, a, "USD")
	eq(t, "typed USD per base", usd.PerBase.String(), "1.05")
	eq(t, "typed USD source", usd.Source, budget.RateSourceManual)
	for _, u := range updates {
		if (u.Code == "USD") != (u.Error != "") {
			t.Errorf("%s: %q", u.Code, u.Error)
		}
	}
	if currency(t, a, "CHF").RateFrom != budget.RateSourceECB {
		t.Fatal("CHF should still update")
	}
}

func usdRate(perBase string, source budget.RateSource) budget.CurrencyRate {
	return budget.CurrencyRate{Code: "USD", PerBase: money.MustDecimal(perBase), Source: source}
}

func TestIsCurrencyChangedOrRemoved(t *testing.T) {
	auto, manual := budget.RateSourceAuto, budget.RateSourceManual
	original := budget.Budget{Currencies: []budget.CurrencyRate{usdRate("1.08", auto)}}
	cases := []struct {
		name    string
		current []budget.CurrencyRate
		want    bool
	}{
		{"unchanged", []budget.CurrencyRate{usdRate("1.08", auto)}, false},
		{"same rate written differently", []budget.CurrencyRate{usdRate("1.080", auto)}, false},
		{"rate typed", []budget.CurrencyRate{usdRate("1.05", auto)}, true},
		{"source switched", []budget.CurrencyRate{usdRate("1.08", manual)}, true},
		{"removed", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			current := budget.Budget{Currencies: c.current}

			got := isCurrencyChangedOrRemoved("USD", original, current)

			eq(t, "changed or removed", got, c.want)
		})
	}
}

func TestPreviewMatchesRefreshAndSavesNothing(t *testing.T) {
	a, ctx := testApp(t), context.Background()
	withSettings(t, a, settings.Patch{DefaultRateSource: ptr(budget.RateSourceCurrencyAPI)})
	before, _ := a.Get(ctx, "home")
	preview, err := a.PreviewRates(ctx, "home", []string{"USD"})
	if err != nil {
		t.Fatal(err)
	}
	if after, _ := a.Get(ctx, "home"); after.Revision != before.Revision {
		t.Fatal("preview saved")
	}
	_, real, err := a.RefreshRates(ctx, byTest, "home", []string{"USD"})
	if err != nil {
		t.Fatal(err)
	}
	// Same source as the real refresh: the default from settings, not plain auto.
	eq(t, "preview source", preview[0].Source, "currency-api")
	eq(t, "preview", fmt.Sprintf("%+v", preview[0]), fmt.Sprintf("%+v", real[0]))
	if _, err := a.PreviewRates(ctx, "home", []string{"XYZ"}); err == nil {
		t.Fatal("unknown currency accepted by preview")
	}
}

func TestEmptySourceMeansDefault(t *testing.T) {
	a, ctx := testApp(t), context.Background()
	empty := budget.RateSource("")
	defaultSource := budget.CurrencyPatch{Source: &empty}
	if _, err := a.SetCurrency(ctx, byTest, "home", "BTC", defaultSource); err != nil {
		t.Fatal(err)
	}
	c := currency(t, a, "BTC")
	eq(t, "BTC source", c.Source, budget.RateSourceAuto)
	eq(t, "BTC is manual", c.IsManual(), false)
}

func TestOutageIsNotReportedAsUnsupported(t *testing.T) {
	a, ctx := testApp(t), context.Background()
	ecb := a.Rates[budget.RateSourceECB]
	down := rates.Fixed{ID: budget.RateSourceCurrencyAPI, Err: errors.New("HTTP 503")}
	a.Rates[budget.RateSourceAuto] = rates.Chain{
		ID:        budget.RateSourceAuto,
		Providers: []rates.Provider{ecb, down},
	}
	q, err := a.Quote(ctx, "EUR", "auto", []string{"USD", "BTC"})
	if err != nil {
		t.Fatalf("USD was fetched, so this is not a total failure: %v", err)
	}
	eq(t, "USD error", q[0].Error, "")
	if !strings.Contains(q[1].Error, "try again") {
		t.Fatalf("BTC error %q does not say to try again", q[1].Error)
	}
	if !strings.Contains(q[1].Detail, "503") {
		t.Fatalf("BTC detail %q does not name the HTTP status", q[1].Detail)
	}
}
