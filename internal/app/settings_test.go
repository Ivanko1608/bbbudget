package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bbbudget/internal/budget"
	"bbbudget/internal/money"
	"bbbudget/internal/rates"
	"bbbudget/internal/settings"
	"bbbudget/internal/store"
)

var quiet = slog.New(slog.DiscardHandler)

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
	noFile, err := a.Settings()
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "settings without a file", noFile, settings.Defaults())
	withSettings(t, a, settings.Patch{AutoRefresh: ptr("daily")})
	updated, _ := a.Settings()
	eq(t, "auto refresh", updated.AutoRefresh, "daily")
	eq(t, "fetch rates", updated.FetchRates, false)
	badSource := settings.Patch{DefaultRateSource: ptr(budget.RateSource("google"))}
	if _, err := a.UpdateSettings(badSource); !errors.Is(err, ErrBadInput) {
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
	_, err := a.SetCurrency(ctx, byTest, "home", "BTC", budget.CurrencyPatch{})
	if !errors.Is(err, ErrRatesOff) {
		t.Fatalf("SetCurrency: %v", err)
	}
	_, _, err = a.RefreshRates(ctx, byTest, "home", []string{"USD"})
	if !errors.Is(err, ErrRatesOff) {
		t.Fatalf("RefreshRates: %v", err)
	}
	if after, _ := a.Get(ctx, "home"); after.Revision != before.Revision {
		t.Fatal("nothing may be written while offline")
	}
	// Typed rates still work.
	typed := budget.CurrencyPatch{PerBase: ptr(money.MustDecimal("0.00001"))}
	if _, err := a.SetCurrency(ctx, byTest, "home", "BTC", typed); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultSourceFromSettings(t *testing.T) {
	a, ctx := testApp(t), context.Background()
	withSettings(t, a, settings.Patch{DefaultRateSource: ptr(budget.RateSource("currency-api"))})
	if _, err := a.SetCurrency(ctx, byTest, "home", "BTC", budget.CurrencyPatch{}); err != nil {
		t.Fatal(err)
	}
	btc := currency(t, a, "BTC")
	eq(t, "BTC source", btc.Source, "currency-api")
	eq(t, "BTC rate provider", btc.RateFrom, "currency-api")
	// Naming a manual currency in a refresh switches it to the default too.
	if _, _, err := a.RefreshRates(ctx, byTest, "home", []string{"USD"}); err != nil {
		t.Fatal(err)
	}
	usd := currency(t, a, "USD")
	eq(t, "USD source", usd.Source, "currency-api")
	eq(t, "USD per base", usd.PerBase.String(), "1.12")
	// A "manual" default still fetches when a fetch is asked for, via auto.
	withSettings(t, a, settings.Patch{DefaultRateSource: ptr(budget.RateSource("manual"))})
	if _, _, err := a.RefreshRates(ctx, byTest, "home", []string{"CHF"}); err != nil {
		t.Fatal(err)
	}
	if source := currency(t, a, "CHF").Source; source != "auto" {
		t.Fatalf("CHF source = %q, want auto", source)
	}
}

func TestRefreshIfDue(t *testing.T) {
	a, ctx := testApp(t), context.Background()
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	a.Now = func() time.Time { return now }
	allManual := "plain"
	if _, err := a.Create(ctx, byTest, english, allManual, "Plain", "example"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetCurrency(ctx, byTest, "home", "BTC", budget.CurrencyPatch{}); err != nil {
		t.Fatal(err)
	}
	gecko := budget.RateSourceCoinGecko
	toGecko := budget.CurrencyPatch{Source: &gecko}
	if _, err := a.SetCurrency(ctx, byTest, "home", "USD", toGecko); err == nil {
		t.Fatal("coingecko is down in the test app") // so USD stays manual
	}

	ran, err := a.RefreshIfDue(ctx, quiet)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "ran with auto-refresh off by default", ran, false)
	withSettings(t, a, settings.Patch{AutoRefresh: ptr("hourly")})
	ran, err = a.RefreshIfDue(ctx, quiet)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "first run happened", ran, true)
	st := a.AutoRefresh()
	eq(t, "last run", st.LastRun, now)
	eq(t, "budgets refreshed", st.Budgets, 1)
	eq(t, "currencies updated", st.Updated, 1)
	eq(t, "failures", len(st.Failures), 0)
	now = now.Add(59 * time.Minute)
	if ran, _ = a.RefreshIfDue(ctx, quiet); ran {
		t.Fatal("ran before the hour was up")
	}
	now = now.Add(time.Minute)
	// Make BTC fail on the next run: the failure is reported, not fatal.
	btcToGecko := setCurrency("BTC", toGecko)
	if _, err = a.Apply(ctx, byTest, "home", store.AnyRevision, btcToGecko); err != nil {
		t.Fatal(err)
	}
	ran, err = a.RefreshIfDue(ctx, quiet)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "second run happened", ran, true)
	st = a.AutoRefresh()
	eq(t, "failures", len(st.Failures), 1)
	eq(t, "currencies updated", st.Updated, 0)

	withSettings(t, a, settings.Patch{FetchRates: ptr(false)})
	now = now.Add(2 * time.Hour)
	if ran, _ := a.RefreshIfDue(ctx, quiet); ran {
		t.Fatal("auto-refresh ran with fetching turned off")
	}
}

func TestCoinGeckoKeyComesFromSettings(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("x-cg-demo-api-key")
		_, _ = w.Write([]byte(`{"bitcoin":{"eur":70000}}`))
	}))
	defer srv.Close()

	a := New(nil, nil) // the real registry, as the binary builds it
	a.SettingsPath = filepath.Join(t.TempDir(), "settings.json")
	withSettings(t, a, settings.Patch{FetchRates: ptr(true), CoinGeckoAPIKey: ptr("CG-test")})
	cg := a.Rates[budget.RateSourceCoinGecko].(*rates.Cache).Provider.(*rates.CoinGeckoProvider)
	cg.URL = srv.URL
	q, err := a.Quote(context.Background(), "EUR", "coingecko", []string{"BTC"})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "BTC quote", q[0].New.String(), "0.00001428571429")
	if k := <-got; k != "CG-test" {
		t.Fatalf("key header %q", k)
	}
}

func TestFailedAutoRefreshRetriesSoon(t *testing.T) {
	a, ctx := testApp(t), context.Background()
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	a.Now = func() time.Time { return now }
	if _, _, err := a.RefreshRates(ctx, byTest, "home", []string{"USD"}); err != nil {
		t.Fatal(err)
	}
	withSettings(t, a, settings.Patch{AutoRefresh: ptr("daily")})
	down := rates.Fixed{ID: budget.RateSourceAuto, Err: errors.New("network is unreachable")}
	up := a.Rates[budget.RateSourceAuto]
	a.Rates[budget.RateSourceAuto] = down

	ran, _ := a.RefreshIfDue(ctx, quiet)
	eq(t, "first run happened", ran, true)
	eq(t, "currencies updated while down", a.AutoRefresh().Updated, 0)
	now = now.Add(9 * time.Minute)
	ran, _ = a.RefreshIfDue(ctx, quiet)
	eq(t, "retried too soon", ran, false)
	a.Rates[budget.RateSourceAuto] = up
	now = now.Add(time.Minute)
	ran, _ = a.RefreshIfDue(ctx, quiet)
	eq(t, "retried after 10 minutes", ran, true)
	eq(t, "currencies updated after retry", a.AutoRefresh().Updated, 1)
	now = now.Add(time.Hour)
	ran, _ = a.RefreshIfDue(ctx, quiet)
	eq(t, "ran again within the full interval", ran, false)
}

func TestAutoRefreshStatusKeepsErrorTextOutOfFailures(t *testing.T) {
	a, ctx := testApp(t), context.Background()
	if _, _, err := a.RefreshRates(ctx, byTest, "home", []string{"USD"}); err != nil {
		t.Fatal(err)
	}
	withSettings(t, a, settings.Patch{AutoRefresh: ptr("daily")})
	const upstreamText = "dial tcp 10.0.0.7:443: network is unreachable"
	a.Rates[budget.RateSourceAuto] = rates.Fixed{
		ID:  budget.RateSourceAuto,
		Err: errors.New(upstreamText),
	}
	var logged bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logged, nil))

	_, err := a.RefreshIfDue(ctx, logger)
	if err != nil {
		t.Fatal(err)
	}

	failures := strings.Join(a.AutoRefresh().Failures, "\n")
	eq(t, "failures are reported", failures != "", true)
	eq(t, "failures hold the upstream text", strings.Contains(failures, upstreamText), false)
	eq(t, "log holds the upstream text", strings.Contains(logged.String(), upstreamText), true)
}
