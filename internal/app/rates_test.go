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
	ecb := rates.Fixed{ID: rates.ECB, Base: "EUR", Date: "2026-10-07", Rates: map[string]string{"USD": "1.1186", "CHF": "0.9326"}}
	capi := rates.Fixed{ID: rates.CurrencyAPI, Base: "EUR", Date: "2026-10-08", Rates: map[string]string{"USD": "1.12", "BTC": "1.35211580123e-05"}}
	a.Rates = map[string]rates.Provider{
		rates.ECB: ecb, rates.CurrencyAPI: capi,
		rates.CoinGecko: rates.Fixed{ID: rates.CoinGecko, Err: errors.New("HTTP 429")},
		rates.Auto:      rates.Chain{ID: rates.Auto, Providers: []rates.Provider{ecb, capi}},
	}
	if _, err := a.Create(context.Background(), byTest, english, "home", "Home", "example"); err != nil {
		t.Fatal(err)
	}
	withSettings(t, a, settings.Patch{FetchRates: ptr(true)}) // off by default; these tests fetch
	return a
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
		if _, ok := reg[s]; !ok && s != budget.SourceManual {
			t.Errorf("source %q has no provider", s)
		}
	}
}

func TestRefreshSkipsManualUnlessNamed(t *testing.T) {
	a, ctx := testApp(t), context.Background()
	v, updates, err := a.RefreshRates(ctx, byTest, "home", nil)
	if err != nil || len(updates) != 0 || currency(t, a, "USD").PerBase.String() != "1.08" {
		t.Fatalf("manual rates must not change: %v %v", updates, err)
	}
	rev := v.Revision

	v, updates, err = a.RefreshRates(ctx, byTest, "home", []string{"usd"})
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 || updates[0].Old.String() != "1.08" || updates[0].New.String() != "1.1186" || updates[0].From != rates.ECB {
		t.Fatalf("updates: %+v", updates)
	}
	usd := currency(t, a, "USD")
	if usd.Source != budget.SourceAuto || usd.RateDate != "2026-10-07" || usd.RateFrom != rates.ECB || usd.PerBase.String() != "1.1186" {
		t.Fatalf("saved: %+v", usd)
	}
	if v.Revision != rev+1 || currency(t, a, "CHF").PerBase.String() != "0.94" {
		t.Fatal("only USD should change, in one write")
	}

	// From now on a plain refresh includes USD.
	_, updates, _ = a.RefreshRates(ctx, byTest, "home", nil)
	if len(updates) != 1 || updates[0].Code != "USD" {
		t.Fatalf("%+v", updates)
	}
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
	if btc.PerBase.String() != "0.00001352115801" || btc.Source != budget.SourceAuto || btc.RateFrom != rates.CurrencyAPI {
		t.Fatalf("got %+v", btc)
	}

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
	if source := currency(t, a, "ETH").Source; source != budget.SourceManual {
		t.Fatalf("ETH source = %q, want %q", source, budget.SourceManual)
	}
}

func TestPartialAndTotalFailure(t *testing.T) {
	a, ctx := testApp(t), context.Background()
	gecko := rates.CoinGecko
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
	if updates[i].Error == "" || currency(t, a, "BTC").PerBase.String() != before || currency(t, a, "USD").PerBase.String() != "1.1186" {
		t.Fatalf("%+v", updates)
	}

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
	for _, c := range []struct{ base, source, code string }{{"EUR", "google", "USD"}, {"EUR", "", "XXX"}, {"ZZZ", "", "USD"}} {
		if _, err := a.Quote(context.Background(), c.base, c.source, []string{c.code}); !errors.Is(err, ErrBadInput) {
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

func (h hook) Quotes(ctx context.Context, base string, codes []string) (map[string]rates.Quote, error) {
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
	a.Rates[rates.Auto] = hook{a.Rates[rates.Auto], func() {
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
	if usd.PerBase.String() != "1.05" || usd.Source != budget.SourceManual {
		t.Fatalf("the typed rate was overwritten: %+v", usd)
	}
	for _, u := range updates {
		if (u.Code == "USD") != (u.Error != "") {
			t.Errorf("%s: %q", u.Code, u.Error)
		}
	}
	if currency(t, a, "CHF").RateFrom != rates.ECB {
		t.Fatal("CHF should still update")
	}
}

func TestPreviewMatchesRefreshAndSavesNothing(t *testing.T) {
	a, ctx := testApp(t), context.Background()
	withSettings(t, a, settings.Patch{DefaultRateSource: ptr("currency-api")})
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
	if preview[0].Source != "currency-api" || fmt.Sprintf("%+v", preview[0]) != fmt.Sprintf("%+v", real[0]) {
		t.Fatalf("preview %+v vs refresh %+v", preview[0], real[0])
	}
	if _, err := a.PreviewRates(ctx, "home", []string{"XYZ"}); err == nil {
		t.Fatal("unknown currency accepted by preview")
	}
}

func TestEmptySourceMeansDefault(t *testing.T) {
	a, ctx := testApp(t), context.Background()
	empty := ""
	defaultSource := budget.CurrencyPatch{Source: &empty}
	if _, err := a.SetCurrency(ctx, byTest, "home", "BTC", defaultSource); err != nil {
		t.Fatal(err)
	}
	if c := currency(t, a, "BTC"); c.Source != budget.SourceAuto || c.IsManual() {
		t.Fatalf("a fetched rate must not be saved as manual: %+v", c)
	}
}

func TestOutageIsNotReportedAsUnsupported(t *testing.T) {
	a, ctx := testApp(t), context.Background()
	ecb := a.Rates[rates.ECB]
	a.Rates[rates.Auto] = rates.Chain{ID: rates.Auto, Providers: []rates.Provider{ecb, rates.Fixed{ID: rates.CurrencyAPI, Err: errors.New("HTTP 503")}}}
	q, err := a.Quote(ctx, "EUR", "auto", []string{"USD", "BTC"})
	if err != nil {
		t.Fatalf("USD was fetched, so this is not a total failure: %v", err)
	}
	if q[0].Error != "" || !strings.Contains(q[1].Error, "try again") || !strings.Contains(q[1].Error, "503") {
		t.Fatalf("%+v", q)
	}
}
