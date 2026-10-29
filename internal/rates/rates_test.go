package rates

import (
	"context"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bbbudget/internal/budget"
)

const ecbXML = `<?xml version="1.0" encoding="UTF-8"?>
<gesmes:Envelope xmlns:gesmes="http://www.gesmes.org/xml/2002-08-01" xmlns="http://www.ecb.int/vocabulary/2002-08-01/eurofxref">
	<gesmes:subject>Reference rates</gesmes:subject>
	<Cube>
		<Cube time='2026-10-07'>
			<Cube currency='USD' rate='1.1186'/>
			<Cube currency='JPY' rate='177.05'/>
			<Cube currency='GBP' rate='0.84698'/>
		</Cube>
	</Cube>
</gesmes:Envelope>`

// The real feed sends floats, sometimes with exponents; they must be read exactly.
const currencyAPIJSON = `{"date": "2026-10-07", "eur": {"usd": 1.12049503, "btc": 1.3521158e-05, "usdt": 1.12122692, "bad": -1}}`

func eq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

func eqRat(t *testing.T, what string, got, want *big.Rat) {
	t.Helper()
	if got.Cmp(want) != 0 {
		t.Fatalf("%s = %s, want %s", what, got, want)
	}
}

func serve(t *testing.T, body string, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		if body == "" {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s
}

func rat(s string) *big.Rat {
	r, _ := new(big.Rat).SetString(s)
	return r
}

func TestECB(t *testing.T) {
	p := &ECBProvider{URL: serve(t, ecbXML, nil).URL}
	q, err := p.Quotes(context.Background(), "EUR", []string{"USD", "GBP", "BTC"})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "quote count", len(q), 2)
	eqRat(t, "USD per EUR", q["USD"].PerBase, rat("1.1186"))
	eq(t, "USD date", q["USD"].Date, "2026-10-07")
	eq(t, "USD source", q["USD"].From, budget.RateSourceECB)
	// Cross rate through the euro, exact: USD per GBP = 1.1186 / 0.84698.
	q, err = p.Quotes(context.Background(), "GBP", []string{"USD", "EUR"})
	if err != nil {
		t.Fatal(err)
	}
	eqRat(t, "USD per GBP", q["USD"].PerBase, new(big.Rat).Quo(rat("1.1186"), rat("0.84698")))
	eqRat(t, "EUR per GBP", q["EUR"].PerBase, new(big.Rat).Inv(rat("0.84698")))
	// A base the ECB doesn't publish yields nothing, not an error.
	q, err = p.Quotes(context.Background(), "KZT", []string{"USD"})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "quote count for an unpublished base", len(q), 0)
}

func TestCurrencyAPIFallsBackToMirror(t *testing.T) {
	down := serve(t, "", nil)
	up := serve(t, currencyAPIJSON, nil)
	p := &CurrencyAPIProvider{URLs: []string{down.URL + "/%s.json", up.URL + "/%s.json"}}
	q, err := p.Quotes(context.Background(), "EUR", []string{"USD", "BTC", "USDT", "BAD", "XYZ"})
	if err != nil {
		t.Fatal(err)
	}
	if len(q) != 3 {
		t.Fatalf("want USD, BTC, USDT; got %v", q)
	}
	eqRat(t, "BTC per EUR", q["BTC"].PerBase, rat("0.000013521158"))
	eq(t, "BTC date", q["BTC"].Date, "2026-10-07")
}

func TestCoinGeckoInvertsPrice(t *testing.T) {
	var gotQuery string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"bitcoin":{"eur":72565},"tether":{"eur":0.893}}`))
	}))
	defer s.Close()
	p := &CoinGeckoProvider{URL: s.URL, Now: func() time.Time { return time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC) }}
	q, err := p.Quotes(context.Background(), "EUR", []string{"BTC", "USDT", "USD"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "ids=bitcoin%2Ctether") {
		t.Fatalf("query %q does not ask for bitcoin and tether", gotQuery)
	}
	if !strings.Contains(gotQuery, "vs_currencies=eur") {
		t.Fatalf("query %q does not ask for euro prices", gotQuery)
	}
	eq(t, "quote count", len(q), 2)
	eqRat(t, "BTC per EUR", q["BTC"].PerBase, big.NewRat(1, 72565))
	eq(t, "BTC date", q["BTC"].Date, "2026-10-07")
}

func TestChainFillsGapsAndFailsOnlyWhenAllFail(t *testing.T) {
	ecb := &ECBProvider{URL: serve(t, ecbXML, nil).URL}
	capi := &CurrencyAPIProvider{URLs: []string{serve(t, currencyAPIJSON, nil).URL + "/%s.json"}}
	q, err := Chain{ID: budget.RateSourceAuto, Providers: []Provider{ecb, capi}}.Quotes(context.Background(), "EUR", []string{"USD", "BTC"})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "USD source", q["USD"].From, budget.RateSourceECB)
	eq(t, "BTC source", q["BTC"].From, budget.RateSourceCurrencyAPI)

	broken := &ECBProvider{URL: serve(t, "", nil).URL}
	q, err = Chain{ID: budget.RateSourceAuto, Providers: []Provider{broken, capi}}.Quotes(context.Background(), "EUR", []string{"USD"})
	if err != nil {
		t.Fatalf("fallback: %v", err)
	}
	eq(t, "fallback USD source", q["USD"].From, budget.RateSourceCurrencyAPI)
	_, err = Chain{ID: budget.RateSourceAuto, Providers: []Provider{broken}}.Quotes(context.Background(), "EUR", []string{"USD"})
	var fe *FetchError
	if !errors.As(err, &fe) {
		t.Fatalf("want a FetchError, got %v", err)
	}
	eq(t, "failing provider", fe.Provider, budget.RateSourceECB)
}

func TestCache(t *testing.T) {
	var hits atomic.Int32
	now := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	c := NewCache(&ECBProvider{URL: serve(t, ecbXML, &hits).URL}, time.Hour)
	c.Now = func() time.Time { return now }
	for range 3 {
		if _, err := c.Quotes(context.Background(), "EUR", []string{"USD"}); err != nil {
			t.Fatal(err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("%d fetches, want 1", hits.Load())
	}
	now = now.Add(2 * time.Hour)
	if _, err := c.Quotes(context.Background(), "EUR", []string{"USD"}); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatal("expired entry not refetched")
	}
}

func TestParseRate(t *testing.T) {
	for in, ok := range map[string]bool{
		"1.1186": true, "1.35e-05": true, "2E3": true,
		"0": false, "-1": false, "1/3": false, "0x10": false, "1e999999": false, "": false, "NaN": false,
	} {
		if _, got := parseRate(in); got != ok {
			t.Errorf("parseRate(%q) ok = %v, want %v", in, got, ok)
		}
	}
}

func TestProvidersReportUpstreamProblems(t *testing.T) {
	ctx := context.Background()
	huge := strings.Repeat(" ", maxResponse+10)
	for name, p := range map[string]Provider{
		"ecb http error":      &ECBProvider{URL: serve(t, "", nil).URL},
		"ecb not xml":         &ECBProvider{URL: serve(t, "<html>maintenance", nil).URL},
		"ecb no rates":        &ECBProvider{URL: serve(t, "<Envelope><Cube></Cube></Envelope>", nil).URL},
		"ecb too large":       &ECBProvider{URL: serve(t, huge, nil).URL},
		"currency-api no eur": &CurrencyAPIProvider{URLs: []string{serve(t, `{"date":"2026-10-07","usd":{}}`, nil).URL + "/%s.json"}},
		"currency-api all down": &CurrencyAPIProvider{URLs: []string{
			serve(t, "", nil).URL + "/%s.json", serve(t, "", nil).URL + "/%s.json"}},
		"coingecko not json": &CoinGeckoProvider{URL: serve(t, "rate limited", nil).URL},
		"unreachable":        &ECBProvider{URL: "http://127.0.0.1:1/"},
	} {
		_, err := p.Quotes(ctx, "EUR", []string{"USD", "BTC"})
		var fe *FetchError
		if !errors.As(err, &fe) {
			t.Errorf("%s: want a FetchError, got %v", name, err)
		}
	}
}

func TestCoinGeckoKey(t *testing.T) {
	var key string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("x-cg-demo-api-key")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer s.Close()
	ctx := context.Background()

	t.Setenv("COINGECKO_API_KEY", "from-env")
	_, _ = (&CoinGeckoProvider{URL: s.URL}).Quotes(ctx, "EUR", []string{"BTC"})
	if key != "from-env" {
		t.Fatalf("env fallback: %q", key)
	}
	fromSettings := &CoinGeckoProvider{URL: s.URL, Key: func() string { return "from-settings" }}
	_, _ = fromSettings.Quotes(ctx, "EUR", []string{"BTC"})
	if key != "from-settings" {
		t.Fatalf("Key func wins: %q", key)
	}
	// Only fiat asked for: no request at all.
	key = "untouched"
	q, err := (&CoinGeckoProvider{URL: s.URL}).Quotes(ctx, "EUR", []string{"USD"})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "quote count for fiat only", len(q), 0)
	eq(t, "key after a fiat-only request", key, "untouched")
}

func TestFixed(t *testing.T) {
	f := Fixed{ID: "x", Base: "EUR", Date: "d", Rates: map[string]string{"USD": "1.1"}}
	if q, _ := f.Quotes(context.Background(), "GBP", []string{"USD"}); len(q) != 0 {
		t.Fatal("other base")
	}
	q, _ := f.Quotes(context.Background(), "EUR", []string{"USD", "JPY"})
	eq(t, "quote count", len(q), 1)
	eq(t, "USD source", q["USD"].From, "x")
}

func TestCoinGeckoRejectedKeyIsExplained(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer s.Close()
	_, err := (&CoinGeckoProvider{URL: s.URL, Key: func() string { return "bad" }}).Quotes(context.Background(), "EUR", []string{"BTC"})
	var fe *FetchError
	if !errors.As(err, &fe) {
		t.Fatalf("want a FetchError, got %v", err)
	}
	eq(t, "status", fe.Status, 401)
	if !strings.Contains(err.Error(), "API key was rejected") {
		t.Fatalf("got %v", err)
	}
	// Without a key there is nothing to blame it on.
	t.Setenv("COINGECKO_API_KEY", "")
	_, err = (&CoinGeckoProvider{URL: s.URL}).Quotes(context.Background(), "EUR", []string{"BTC"})
	if err == nil {
		t.Fatal("want an error without a key")
	}
	if strings.Contains(err.Error(), "API key") {
		t.Fatalf("got %v", err)
	}
}

func TestCacheRemembersMisses(t *testing.T) {
	var hits atomic.Int32
	c := NewCache(&ECBProvider{URL: serve(t, ecbXML, &hits).URL}, time.Hour)
	for range 3 {
		q, err := c.Quotes(context.Background(), "EUR", []string{"USD", "BTC"}) // the ECB has no BTC
		if err != nil {
			t.Fatal(err)
		}
		eq(t, "quote count", len(q), 1)
	}
	if hits.Load() != 1 {
		t.Fatalf("%d fetches: a code the provider lacks must not force a refetch", hits.Load())
	}
	// A failed request is not cached as "missing".
	failing := NewCache(Fixed{ID: "x", Err: errors.New("down")}, time.Hour)
	_, _ = failing.Quotes(context.Background(), "EUR", []string{"USD"})
	if len(failing.entries) != 0 {
		t.Fatal("cached a failure")
	}
}

func TestChainReportsOutageForMissingCodes(t *testing.T) {
	ecb := &ECBProvider{URL: serve(t, ecbXML, nil).URL}
	down := &CurrencyAPIProvider{URLs: []string{serve(t, "", nil).URL + "/%s.json"}}
	q, err := Chain{ID: budget.RateSourceAuto, Providers: []Provider{ecb, down}}.Quotes(context.Background(), "EUR", []string{"USD", "BTC"})
	var pe *PartialError
	if !errors.As(err, &pe) {
		t.Fatalf("want a PartialError, got %v", err)
	}
	eq(t, "USD source", q["USD"].From, budget.RateSourceECB)
	eq(t, "quote count", len(q), 1)
	// Everything priced: an outage further down the chain doesn't matter.
	if _, err := (Chain{ID: budget.RateSourceAuto, Providers: []Provider{ecb, down}}).Quotes(context.Background(), "EUR", []string{"USD"}); err != nil {
		t.Fatal(err)
	}
}
