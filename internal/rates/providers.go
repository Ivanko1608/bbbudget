package rates

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"bbbudget/internal/budget"
)

// ECBProvider reads the European Central Bank's euro foreign exchange
// reference rates: official, published around 16:00 CET on working days,
// about 30 major currencies, no crypto. Rates for a non-EUR base are cross
// rates through the euro.
type ECBProvider struct {
	URL    string // default: the ECB daily feed
	Client *http.Client
}

const ecbURL = "https://www.ecb.europa.eu/stats/eurofxref/eurofxref-daily.xml"

func (p *ECBProvider) Name() string { return budget.SourceECB }

func (p *ECBProvider) Quotes(ctx context.Context, base string, codes []string) (map[string]Quote, error) {
	u := p.URL
	if u == "" {
		u = ecbURL
	}
	body, err := get(ctx, p.Client, budget.SourceECB, u, nil)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Cube struct {
			Day []struct {
				Time  string `xml:"time,attr"`
				Rates []struct {
					Currency string `xml:"currency,attr"`
					Rate     string `xml:"rate,attr"`
				} `xml:"Cube"`
			} `xml:"Cube"`
		} `xml:"Cube"`
	}
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, &FetchError{Provider: budget.SourceECB, Err: err}
	}
	if len(doc.Cube.Day) == 0 {
		return nil, &FetchError{Provider: budget.SourceECB, Err: errors.New("no rates in response")}
	}
	day := doc.Cube.Day[0]
	perEUR := map[string]*big.Rat{"EUR": big.NewRat(1, 1)}
	for _, r := range day.Rates {
		if v, ok := parseRate(r.Rate); ok {
			perEUR[r.Currency] = v
		}
	}
	out := map[string]Quote{}
	b, ok := perEUR[base]
	if !ok {
		return out, nil // the ECB doesn't quote this base: nothing to offer
	}
	for _, code := range codes {
		if c, ok := perEUR[code]; ok {
			out[code] = Quote{Code: code, PerBase: new(big.Rat).Quo(c, b), Date: day.Time, From: budget.SourceECB}
		}
	}
	return out, nil
}

// CurrencyAPIProvider reads fawazahmed0's free currency-api: daily rates for
// about 300 fiat currencies and cryptocurrencies, aggregated from public
// sources, served from a CDN with a mirror as fallback. No key needed.
type CurrencyAPIProvider struct {
	// URLs are tried in order; %s is the lowercase base code.
	URLs   []string
	Client *http.Client
}

var currencyAPIURLs = []string{
	"https://cdn.jsdelivr.net/npm/@fawazahmed0/currency-api@latest/v1/currencies/%s.json",
	"https://latest.currency-api.pages.dev/v1/currencies/%s.json",
}

func (p *CurrencyAPIProvider) Name() string { return budget.SourceCurrencyAPI }

func (p *CurrencyAPIProvider) Quotes(ctx context.Context, base string, codes []string) (map[string]Quote, error) {
	urls := p.URLs
	if len(urls) == 0 {
		urls = currencyAPIURLs
	}
	lb := strings.ToLower(base)
	var body []byte
	var errs []error
	for _, u := range urls {
		b, err := get(ctx, p.Client, budget.SourceCurrencyAPI, fmt.Sprintf(u, url.PathEscape(lb)), nil)
		if err == nil {
			body = b
			break
		}
		errs = append(errs, err)
	}
	if body == nil {
		return nil, errors.Join(errs...)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, &FetchError{Provider: budget.SourceCurrencyAPI, Err: err}
	}
	var date string
	_ = json.Unmarshal(doc["date"], &date)
	var table map[string]json.Number
	d := json.NewDecoder(bytes.NewReader(doc[lb]))
	d.UseNumber() // keep each rate's literal text: never a float64
	if err := d.Decode(&table); err != nil {
		return nil, &FetchError{Provider: budget.SourceCurrencyAPI, Err: fmt.Errorf("no %s table: %w", base, err)}
	}
	out := map[string]Quote{}
	for _, code := range codes {
		if v, ok := parseRate(table[strings.ToLower(code)].String()); ok {
			out[code] = Quote{Code: code, PerBase: v, Date: date, From: budget.SourceCurrencyAPI}
		}
	}
	return out, nil
}

// CoinGeckoProvider prices cryptocurrencies from CoinGecko's live market data.
// The public API needs no key but is rate-limited; a free demo key (from Key,
// else $COINGECKO_API_KEY) raises the limit.
type CoinGeckoProvider struct {
	URL    string        // default: the public API root
	Key    func() string // may be nil or return ""
	Client *http.Client
	Now    func() time.Time
}

// coinGeckoIDs maps tickers to CoinGecko coin ids.
var coinGeckoIDs = map[string]string{
	"BTC": "bitcoin", "ETH": "ethereum", "SOL": "solana", "LTC": "litecoin", "BNB": "binancecoin",
	"XRP": "ripple", "ADA": "cardano", "TRX": "tron", "DOGE": "dogecoin",
	"USDT": "tether", "USDC": "usd-coin", "DAI": "dai",
}

func (p *CoinGeckoProvider) Name() string { return budget.SourceCoinGecko }

func (p *CoinGeckoProvider) Quotes(ctx context.Context, base string, codes []string) (map[string]Quote, error) {
	var ids []string
	for _, c := range codes {
		if id, ok := coinGeckoIDs[c]; ok {
			ids = append(ids, id)
		}
	}
	out := map[string]Quote{}
	if len(ids) == 0 {
		return out, nil
	}
	root := p.URL
	if root == "" {
		root = "https://api.coingecko.com/api/v3"
	}
	vs := strings.ToLower(base)
	q := url.Values{"ids": {strings.Join(ids, ",")}, "vs_currencies": {vs}, "precision": {"full"}}
	h := http.Header{}
	var key string
	if p.Key != nil {
		key = p.Key()
	}
	if key == "" {
		key = os.Getenv("COINGECKO_API_KEY")
	}
	if key != "" {
		h.Set("x-cg-demo-api-key", key)
	}
	body, err := get(ctx, p.Client, budget.SourceCoinGecko, root+"/simple/price?"+q.Encode(), h)
	var fe *FetchError
	if key != "" && errors.As(err, &fe) && (fe.Status == http.StatusUnauthorized || fe.Status == http.StatusForbidden) {
		return nil, &FetchError{Provider: budget.SourceCoinGecko, Err: fmt.Errorf("HTTP %d: the API key was rejected; fix or remove it in Settings", fe.Status), Status: fe.Status}
	}
	if err != nil {
		return nil, err
	}
	var prices map[string]map[string]json.Number
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	if err := d.Decode(&prices); err != nil {
		return nil, &FetchError{Provider: budget.SourceCoinGecko, Err: err}
	}
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	date := now().UTC().Format("2006-01-02")
	for _, code := range codes {
		price, ok := parseRate(prices[coinGeckoIDs[code]][vs].String())
		if !ok {
			continue
		}
		// CoinGecko quotes the coin's price in base; a budget wants coins per base unit.
		out[code] = Quote{Code: code, PerBase: new(big.Rat).Inv(price), Date: date, From: budget.SourceCoinGecko}
	}
	return out, nil
}
