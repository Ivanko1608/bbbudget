// Package money provides exact monetary arithmetic.
//
// Amounts are int64 counts of a currency's minor unit (cents, kopecks, ...).
// Exchange rates and percentages are exact decimals (math/big.Rat). Floating
// point is never used. Every place a fractional minor unit can appear is an
// explicit rounding call with a named RoundingMode.
package money

import (
	"fmt"
	"maps"
	"slices"
)

// Currency is an ISO 4217 code (or a crypto ticker) with its number of minor-unit digits.
type Currency struct {
	Code   string
	Digits int
	Symbol string
	// SymbolFirst renders "€12" rather than "12 ₽".
	SymbolFirst bool
	// Crypto marks a cryptocurrency.
	Crypto bool
	// Stable marks a cryptocurrency pegged to a fiat currency.
	Stable bool
}

// Built-in table: ISO 4217 fiat currencies plus common cryptocurrencies. Extend here to support more currencies.
var currencies = map[string]Currency{
	"EUR": {Code: "EUR", Digits: 2, Symbol: "€", SymbolFirst: true},
	"USD": {Code: "USD", Digits: 2, Symbol: "$", SymbolFirst: true},
	"GBP": {Code: "GBP", Digits: 2, Symbol: "£", SymbolFirst: true},
	"CHF": {Code: "CHF", Digits: 2, Symbol: "CHF"},
	"RUB": {Code: "RUB", Digits: 2, Symbol: "₽"},
	"AZN": {Code: "AZN", Digits: 2, Symbol: "₼"},
	"GEL": {Code: "GEL", Digits: 2, Symbol: "₾"},
	"TRY": {Code: "TRY", Digits: 2, Symbol: "₺"},
	"KZT": {Code: "KZT", Digits: 2, Symbol: "₸"},
	"UAH": {Code: "UAH", Digits: 2, Symbol: "₴"},
	"PLN": {Code: "PLN", Digits: 2, Symbol: "zł"},
	"CZK": {Code: "CZK", Digits: 2, Symbol: "Kč"},
	"SEK": {Code: "SEK", Digits: 2, Symbol: "kr"},
	"NOK": {Code: "NOK", Digits: 2, Symbol: "kr"},
	"DKK": {Code: "DKK", Digits: 2, Symbol: "kr"},
	"RSD": {Code: "RSD", Digits: 2, Symbol: "RSD"},
	"AMD": {Code: "AMD", Digits: 2, Symbol: "֏"},
	"AED": {Code: "AED", Digits: 2, Symbol: "AED"},
	"INR": {Code: "INR", Digits: 2, Symbol: "₹", SymbolFirst: true},
	"CNY": {Code: "CNY", Digits: 2, Symbol: "¥", SymbolFirst: true},
	"CAD": {Code: "CAD", Digits: 2, Symbol: "CA$", SymbolFirst: true},
	"AUD": {Code: "AUD", Digits: 2, Symbol: "A$", SymbolFirst: true},
	"JPY": {Code: "JPY", Digits: 0, Symbol: "¥", SymbolFirst: true},
	"KRW": {Code: "KRW", Digits: 0, Symbol: "₩", SymbolFirst: true},
	"KWD": {Code: "KWD", Digits: 3, Symbol: "KWD"},
	"BHD": {Code: "BHD", Digits: 3, Symbol: "BHD"},

	// Cryptocurrencies. Digits are what a budget needs, not the chain's full
	// precision (ETH has 18): amounts with more places are rejected as usual.
	"BTC":  {Code: "BTC", Digits: 8, Symbol: "BTC", Crypto: true},
	"ETH":  {Code: "ETH", Digits: 8, Symbol: "ETH", Crypto: true},
	"SOL":  {Code: "SOL", Digits: 8, Symbol: "SOL", Crypto: true},
	"LTC":  {Code: "LTC", Digits: 8, Symbol: "LTC", Crypto: true},
	"BNB":  {Code: "BNB", Digits: 8, Symbol: "BNB", Crypto: true},
	"XRP":  {Code: "XRP", Digits: 6, Symbol: "XRP", Crypto: true},
	"ADA":  {Code: "ADA", Digits: 6, Symbol: "ADA", Crypto: true},
	"TRX":  {Code: "TRX", Digits: 6, Symbol: "TRX", Crypto: true},
	"DOGE": {Code: "DOGE", Digits: 8, Symbol: "DOGE", Crypto: true},
	"USDT": {Code: "USDT", Digits: 6, Symbol: "USDT", Crypto: true, Stable: true},
	"USDC": {Code: "USDC", Digits: 6, Symbol: "USDC", Crypto: true, Stable: true},
	"DAI":  {Code: "DAI", Digits: 6, Symbol: "DAI", Crypto: true, Stable: true},
}

// Lookup returns the currency for an ISO code.
func Lookup(code string) (Currency, error) {
	c, ok := currencies[code]
	if !ok {
		return Currency{}, fmt.Errorf("unsupported currency %q", code)
	}
	return c, nil
}

// MustLookup is Lookup for codes known at compile time.
func MustLookup(code string) Currency {
	c, err := Lookup(code)
	if err != nil {
		panic(err)
	}
	return c
}

// Codes lists supported currency codes, sorted.
func Codes() []string {
	return slices.Sorted(maps.Keys(currencies))
}

// CryptoCodes lists supported cryptocurrency codes, sorted.
func CryptoCodes() []string {
	var out []string
	for _, k := range Codes() {
		if currencies[k].Crypto {
			out = append(out, k)
		}
	}
	return out
}

func (c Currency) String() string { return c.Code }
