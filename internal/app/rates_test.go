package app

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"bbbudget/internal/budget"
	"bbbudget/internal/money"
	"bbbudget/internal/rates"
	"bbbudget/internal/store"
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
	if _, err := a.Create(context.Background(), "home", "Home", "example"); err != nil {
		t.Fatal(err)
	}
	return a
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
	v, updates, err := a.RefreshRates(ctx, "home", nil)
	if err != nil || len(updates) != 0 || currency(t, a, "USD").PerBase.String() != "1.08" {
		t.Fatalf("manual rates must not change: %v %v", updates, err)
	}
	rev := v.Revision

	v, updates, err = a.RefreshRates(ctx, "home", []string{"usd"})
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
	if _, updates, _ = a.RefreshRates(ctx, "home", nil); len(updates) != 1 || updates[0].Code != "USD" {
		t.Fatalf("%+v", updates)
	}
	if _, _, err := a.RefreshRates(ctx, "home", []string{"JPY"}); err == nil {
		t.Fatal("refreshing a currency the budget doesn't have")
	}
}

func TestSetCurrencyFetchesCryptoRate(t *testing.T) {
	a, ctx := testApp(t), context.Background()
	if _, err := a.SetCurrency(ctx, "home", "btc", budget.CurrencyPatch{}); err != nil {
		t.Fatal(err)
	}
	btc := currency(t, a, "BTC")
	// ECB has no BTC, so auto falls back; the rate keeps 10 significant digits.
	if btc.PerBase.String() != "0.00001352115801" || btc.Source != budget.SourceAuto || btc.RateFrom != rates.CurrencyAPI {
		t.Fatalf("got %+v", btc)
	}

	// No provider prices it: a clear error, nothing saved.
	_, err := a.SetCurrency(ctx, "home", "ETH", budget.CurrencyPatch{})
	var re *RateError
	if !errors.As(err, &re) {
		t.Fatalf("want RateError, got %v", err)
	}
	// Unless the rate is given.
	rate := money.MustDecimal("0.0004")
	if _, err := a.SetCurrency(ctx, "home", "ETH", budget.CurrencyPatch{PerBase: &rate}); err != nil || currency(t, a, "ETH").Source != budget.SourceManual {
		t.Fatal(err)
	}
}

func TestPartialAndTotalFailure(t *testing.T) {
	a, ctx := testApp(t), context.Background()
	gecko := rates.CoinGecko
	if _, err := a.SetCurrency(ctx, "home", "BTC", budget.CurrencyPatch{}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(ctx, "home", store.AnyRevision, []budget.Op{budget.NewOp("set_currency", "BTC", budget.CurrencyPatch{Source: &gecko})}); err != nil {
		t.Fatal(err)
	}
	before := currency(t, a, "BTC").PerBase.String()

	_, updates, err := a.RefreshRates(ctx, "home", []string{"USD", "BTC"})
	if err != nil {
		t.Fatalf("one currency failing must not fail the rest: %v", err)
	}
	i := slices.IndexFunc(updates, func(u RateUpdate) bool { return u.Code == "BTC" })
	if updates[i].Error == "" || currency(t, a, "BTC").PerBase.String() != before || currency(t, a, "USD").PerBase.String() != "1.1186" {
		t.Fatalf("%+v", updates)
	}

	v, _ := a.Get(ctx, "home")
	_, _, err = a.RefreshRates(ctx, "home", []string{"BTC"})
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
