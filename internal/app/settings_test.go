package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"bbbudget/internal/budget"
	"bbbudget/internal/money"
	"bbbudget/internal/rates"
	"bbbudget/internal/settings"
)

func ptr[T any](v T) *T { return &v }

// withSettings gives the test app a settings file and applies p.
func withSettings(t *testing.T, a *App, p settings.Patch) {
	t.Helper()
	if a.SettingsPath == "" {
		a.SettingsPath = filepath.Join(t.TempDir(), "settings.json")
	}
	if _, err := a.UpdateSettings(p); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateSettings(t *testing.T) {
	a := testApp(t)
	a.SettingsPath = filepath.Join(t.TempDir(), "settings.json") // no file yet
	if s, err := a.Settings(); err != nil || s != settings.Defaults() {
		t.Fatalf("no file: %+v %v", s, err)
	}
	withSettings(t, a, settings.Patch{AutoRefresh: ptr("daily")})
	if s, _ := a.Settings(); s.AutoRefresh != "daily" || s.FetchRates {
		t.Fatalf("%+v", s)
	}
	if _, err := a.UpdateSettings(settings.Patch{DefaultRateSource: ptr("google")}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("want ErrBadInput, got %v", err)
	}
	if s, _ := a.Settings(); s.AutoRefresh != "daily" {
		t.Fatal("a rejected patch must not change anything")
	}
}

func TestRatesOffIsFullyOffline(t *testing.T) {
	a, ctx := testApp(t), context.Background()
	withSettings(t, a, settings.Patch{FetchRates: ptr(false)})
	before, _ := a.Get(ctx, "home")

	if _, err := a.Quote(ctx, "EUR", "", []string{"USD"}); !errors.Is(err, ErrRatesOff) {
		t.Fatalf("Quote: %v", err)
	}
	if _, err := a.SetCurrency(ctx, "home", "BTC", budget.CurrencyPatch{}); !errors.Is(err, ErrRatesOff) {
		t.Fatalf("SetCurrency: %v", err)
	}
	if _, _, err := a.RefreshRates(ctx, "home", []string{"USD"}); !errors.Is(err, ErrRatesOff) {
		t.Fatalf("RefreshRates: %v", err)
	}
	if after, _ := a.Get(ctx, "home"); after.Revision != before.Revision {
		t.Fatal("nothing may be written while offline")
	}
	// Typed rates still work.
	if _, err := a.SetCurrency(ctx, "home", "BTC", budget.CurrencyPatch{PerBase: ptr(money.MustDecimal("0.00001"))}); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultSourceFromSettings(t *testing.T) {
	a, ctx := testApp(t), context.Background()
	withSettings(t, a, settings.Patch{DefaultRateSource: ptr("currency-api")})
	if _, err := a.SetCurrency(ctx, "home", "BTC", budget.CurrencyPatch{}); err != nil {
		t.Fatal(err)
	}
	if c := currency(t, a, "BTC"); c.Source != "currency-api" || c.RateFrom != "currency-api" {
		t.Fatalf("new currency: %+v", c)
	}
	// Naming a manual currency in a refresh switches it to the default too.
	if _, _, err := a.RefreshRates(ctx, "home", []string{"USD"}); err != nil {
		t.Fatal(err)
	}
	if c := currency(t, a, "USD"); c.Source != "currency-api" || c.PerBase.String() != "1.12" {
		t.Fatalf("USD: %+v", c)
	}
	// A "manual" default still fetches when a fetch is asked for, via auto.
	withSettings(t, a, settings.Patch{DefaultRateSource: ptr("manual")})
	if _, _, err := a.RefreshRates(ctx, "home", []string{"CHF"}); err != nil || currency(t, a, "CHF").Source != "auto" {
		t.Fatalf("CHF: %+v %v", currency(t, a, "CHF"), err)
	}
}

func TestRefreshIfDue(t *testing.T) {
	a, ctx := testApp(t), context.Background()
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	a.Now = func() time.Time { return now }
	if _, err := a.Create(ctx, "plain", "Plain", "example"); err != nil { // all-manual: untouched
		t.Fatal(err)
	}
	if _, err := a.SetCurrency(ctx, "home", "BTC", budget.CurrencyPatch{}); err != nil {
		t.Fatal(err)
	}
	gecko := "coingecko"
	if _, err := a.SetCurrency(ctx, "home", "USD", budget.CurrencyPatch{Source: &gecko}); err == nil {
		t.Fatal("coingecko is down in the test app") // so USD stays manual
	}

	if ran, err := a.RefreshIfDue(ctx); ran || err != nil {
		t.Fatalf("auto-refresh is off by default: %v %v", ran, err)
	}
	withSettings(t, a, settings.Patch{AutoRefresh: ptr("hourly")})
	if ran, err := a.RefreshIfDue(ctx); !ran || err != nil {
		t.Fatalf("first run: %v %v", ran, err)
	}
	st := a.AutoRefresh()
	if !st.LastRun.Equal(now) || st.Budgets != 1 || st.Updated != 1 || len(st.Failures) != 0 {
		t.Fatalf("status: %+v", st)
	}
	now = now.Add(59 * time.Minute)
	if ran, _ := a.RefreshIfDue(ctx); ran {
		t.Fatal("ran before the hour was up")
	}
	now = now.Add(time.Minute)
	// Make BTC fail on the next run: the failure is reported, not fatal.
	if _, err := a.Apply(ctx, "home", -1, []budget.Op{budget.NewOp("set_currency", "BTC", budget.CurrencyPatch{Source: &gecko})}); err != nil {
		t.Fatal(err)
	}
	if ran, err := a.RefreshIfDue(ctx); !ran || err != nil {
		t.Fatalf("second run: %v %v", ran, err)
	}
	if st := a.AutoRefresh(); len(st.Failures) != 1 || st.Updated != 0 {
		t.Fatalf("status: %+v", st)
	}

	withSettings(t, a, settings.Patch{FetchRates: ptr(false)})
	now = now.Add(2 * time.Hour)
	if ran, _ := a.RefreshIfDue(ctx); ran {
		t.Fatal("auto-refresh ran with fetching turned off")
	}
}

func TestCoinGeckoKeyComesFromSettings(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("x-cg-demo-api-key")
		w.Write([]byte(`{"bitcoin":{"eur":70000}}`))
	}))
	defer srv.Close()

	a := New(nil, nil) // the real registry, as the binary builds it
	a.SettingsPath = filepath.Join(t.TempDir(), "settings.json")
	withSettings(t, a, settings.Patch{FetchRates: ptr(true), CoinGeckoAPIKey: ptr("CG-test")})
	cg := a.Rates[rates.CoinGecko].(*rates.Cache).Provider.(*rates.CoinGeckoProvider)
	cg.URL = srv.URL
	q, err := a.Quote(context.Background(), "EUR", "coingecko", []string{"BTC"})
	if err != nil || q[0].New.String() != "0.00001428571429" {
		t.Fatalf("%+v %v", q, err)
	}
	if k := <-got; k != "CG-test" {
		t.Fatalf("key header %q", k)
	}
}

func TestFailedAutoRefreshRetriesSoon(t *testing.T) {
	a, ctx := testApp(t), context.Background()
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	a.Now = func() time.Time { return now }
	if _, _, err := a.RefreshRates(ctx, "home", []string{"USD"}); err != nil {
		t.Fatal(err)
	}
	withSettings(t, a, settings.Patch{AutoRefresh: ptr("daily")})
	down := rates.Fixed{ID: rates.Auto, Err: errors.New("network is unreachable")}
	up := a.Rates[rates.Auto]
	a.Rates[rates.Auto] = down

	if ran, _ := a.RefreshIfDue(ctx); !ran || a.AutoRefresh().Updated != 0 {
		t.Fatal("first run should run and fail")
	}
	now = now.Add(9 * time.Minute)
	if ran, _ := a.RefreshIfDue(ctx); ran {
		t.Fatal("retried too soon")
	}
	a.Rates[rates.Auto] = up
	now = now.Add(time.Minute)
	if ran, _ := a.RefreshIfDue(ctx); !ran || a.AutoRefresh().Updated != 1 {
		t.Fatalf("a failed run must be retried after 10 minutes, not a day: %+v", a.AutoRefresh())
	}
	now = now.Add(time.Hour)
	if ran, _ := a.RefreshIfDue(ctx); ran {
		t.Fatal("after a good run the full interval applies again")
	}
}
