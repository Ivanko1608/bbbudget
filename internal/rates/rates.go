// Package rates fetches exchange rates from public sources, so a budget's
// rates can follow the market instead of being typed by hand.
//
// Rates stay exact: provider numbers are parsed from their literal text into
// big.Rat (exponents included), never through float64. A Quote is always
// "units of Code per 1 unit of base", the convention budgets store.
package rates

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Provider names. They match the budget's currency "source" values.
const (
	Auto        = "auto"
	ECB         = "ecb"
	CurrencyAPI = "currency-api"
	CoinGecko   = "coingecko"
)

// Quote is one exchange rate.
type Quote struct {
	Code string
	// PerBase is units of Code per 1 unit of the base currency.
	PerBase *big.Rat
	// Date is the day the provider published the rate (YYYY-MM-DD).
	Date string
	// From is the provider that supplied it.
	From string
}

// Provider fetches rates. Codes the provider doesn't price are left out of
// the result rather than reported as errors, so providers can be chained.
type Provider interface {
	Name() string
	Quotes(ctx context.Context, base string, codes []string) (map[string]Quote, error)
}

// FetchError is a provider that could not be reached or returned something
// unusable: an upstream problem, not bad input.
type FetchError struct {
	Provider string
	Err      error
	// Status is the HTTP status when the provider answered with an error.
	Status int
}

func (e *FetchError) Error() string { return e.Provider + ": " + e.Err.Error() }
func (e *FetchError) Unwrap() error { return e.Err }

// maxResponse bounds any provider response.
const maxResponse = 4 << 20

// get fetches url and returns the body, or a FetchError.
func get(ctx context.Context, c *http.Client, provider, url string, header http.Header) ([]byte, error) {
	if c == nil {
		c = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, &FetchError{Provider: provider, Err: err}
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("User-Agent", "bbbudget")
	resp, err := c.Do(req)
	if err != nil {
		return nil, &FetchError{Provider: provider, Err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return nil, &FetchError{Provider: provider, Err: err}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &FetchError{Provider: provider, Err: fmt.Errorf("HTTP %d", resp.StatusCode), Status: resp.StatusCode}
	}
	if len(body) > maxResponse {
		return nil, &FetchError{Provider: provider, Err: errors.New("response too large")}
	}
	return body, nil
}

// parseRate reads a positive decimal from its literal text ("1.1186",
// "1.35e-05"). Fractions, hex and huge exponents (which big.Rat would expand
// into a huge integer) are refused.
func parseRate(s string) (*big.Rat, bool) {
	s = strings.TrimSpace(s)
	mant, exp, hasExp := strings.Cut(strings.ToLower(s), "e")
	if len(s) > 64 || mant == "" || strings.Trim(mant, "0123456789.") != "" {
		return nil, false
	}
	if hasExp {
		if n, err := strconv.Atoi(exp); err != nil || n < -30 || n > 30 {
			return nil, false
		}
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok || r.Sign() <= 0 {
		return nil, false
	}
	return r, true
}

// Chain asks each provider in turn for the codes still missing. It fails only
// if every provider failed; partial answers are returned as they are.
type Chain struct {
	ID        string
	Providers []Provider
}

func (c Chain) Name() string { return c.ID }

func (c Chain) Quotes(ctx context.Context, base string, codes []string) (map[string]Quote, error) {
	out := map[string]Quote{}
	var errs []error
	for _, p := range c.Providers {
		var missing []string
		for _, code := range codes {
			if _, ok := out[code]; !ok {
				missing = append(missing, code)
			}
		}
		if len(missing) == 0 {
			break
		}
		q, err := p.Quotes(ctx, base, missing)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for k, v := range q {
			out[k] = v
		}
	}
	if len(out) == 0 && len(errs) == len(c.Providers) {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

// Cache remembers quotes for TTL, so refreshing several budgets (or clicking
// twice) doesn't hit the provider each time. Safe for concurrent use.
type Cache struct {
	Provider
	TTL time.Duration
	Now func() time.Time

	mu      sync.Mutex
	entries map[string]cached
}

type cached struct {
	q  Quote
	at time.Time
}

// NewCache wraps p.
func NewCache(p Provider, ttl time.Duration) *Cache {
	return &Cache{Provider: p, TTL: ttl, Now: time.Now, entries: map[string]cached{}}
}

func (c *Cache) Quotes(ctx context.Context, base string, codes []string) (map[string]Quote, error) {
	out := map[string]Quote{}
	var missing []string
	now := c.Now()
	c.mu.Lock()
	for _, code := range codes {
		if e, ok := c.entries[base+"/"+code]; ok && now.Sub(e.at) < c.TTL {
			out[code] = e.q
		} else {
			missing = append(missing, code)
		}
	}
	c.mu.Unlock()
	if len(missing) == 0 {
		return out, nil
	}
	q, err := c.Provider.Quotes(ctx, base, missing)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range q {
		c.entries[base+"/"+k] = cached{v, now}
		out[k] = v
	}
	return out, nil
}

// Registry returns every provider by name, each cached for an hour, using
// client for HTTP (nil means http.DefaultClient). "auto" prefers the ECB's
// official reference rates and falls back to currency-api, which also covers
// crypto and the currencies the ECB doesn't publish. coinGeckoKey, if not nil,
// supplies CoinGecko's API key at request time.
func Registry(client *http.Client, coinGeckoKey func() string) map[string]Provider {
	ecb := NewCache(&ECBProvider{Client: client}, time.Hour)
	capi := NewCache(&CurrencyAPIProvider{Client: client}, time.Hour)
	cg := NewCache(&CoinGeckoProvider{Client: client, Key: coinGeckoKey}, time.Hour)
	return map[string]Provider{
		ECB:         ecb,
		CurrencyAPI: capi,
		CoinGecko:   cg,
		Auto:        Chain{ID: Auto, Providers: []Provider{ecb, capi}},
	}
}

// Fixed is a provider with set rates for one base currency, for tests and
// offline use. Rates are decimal strings, units per 1 base; Err, if set, is
// returned instead.
type Fixed struct {
	ID, Base, Date string
	Rates          map[string]string
	Err            error
}

func (f Fixed) Name() string { return f.ID }

func (f Fixed) Quotes(_ context.Context, base string, codes []string) (map[string]Quote, error) {
	if f.Err != nil {
		return nil, &FetchError{Provider: f.ID, Err: f.Err}
	}
	out := map[string]Quote{}
	if base != f.Base {
		return out, nil
	}
	for _, c := range codes {
		if v, ok := parseRate(f.Rates[c]); ok {
			out[c] = Quote{Code: c, PerBase: v, Date: f.Date, From: f.ID}
		}
	}
	return out, nil
}
