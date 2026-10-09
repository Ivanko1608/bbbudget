// Package money provides exact monetary arithmetic.
//
// Amounts are int64 counts of a currency's minor unit (cents, kopecks, ...).
// Exchange rates and percentages are exact decimals (math/big.Rat). Floating
// point is never used. Every place a fractional minor unit can appear is an
// explicit rounding call with a named RoundingMode.
package money

import (
	"fmt"
	"sort"
)

// Currency is an ISO 4217 code (or a crypto ticker) with its number of minor-unit digits.
type Currency struct {
	Code   string
	Digits int
	Symbol string
	// SymbolFirst renders "€12" rather than "12 ₽".
	SymbolFirst bool
	// Crypto marks a cryptocurrency. Stable marks one pegged to a fiat currency.
	Crypto, Stable bool
}

// Built-in table: ISO 4217 fiat currencies plus common cryptocurrencies. Extend here to support more currencies.
var currencies = map[string]Currency{
	"EUR": {"EUR", 2, "€", true, false, false},
	"USD": {"USD", 2, "$", true, false, false},
	"GBP": {"GBP", 2, "£", true, false, false},
	"CHF": {"CHF", 2, "CHF", false, false, false},
	"RUB": {"RUB", 2, "₽", false, false, false},
	"AZN": {"AZN", 2, "₼", false, false, false},
	"GEL": {"GEL", 2, "₾", false, false, false},
	"TRY": {"TRY", 2, "₺", false, false, false},
	"KZT": {"KZT", 2, "₸", false, false, false},
	"UAH": {"UAH", 2, "₴", false, false, false},
	"PLN": {"PLN", 2, "zł", false, false, false},
	"CZK": {"CZK", 2, "Kč", false, false, false},
	"SEK": {"SEK", 2, "kr", false, false, false},
	"NOK": {"NOK", 2, "kr", false, false, false},
	"DKK": {"DKK", 2, "kr", false, false, false},
	"RSD": {"RSD", 2, "RSD", false, false, false},
	"AMD": {"AMD", 2, "֏", false, false, false},
	"AED": {"AED", 2, "AED", false, false, false},
	"INR": {"INR", 2, "₹", true, false, false},
	"CNY": {"CNY", 2, "¥", true, false, false},
	"CAD": {"CAD", 2, "CA$", true, false, false},
	"AUD": {"AUD", 2, "A$", true, false, false},
	"JPY": {"JPY", 0, "¥", true, false, false},
	"KRW": {"KRW", 0, "₩", true, false, false},
	"KWD": {"KWD", 3, "KWD", false, false, false},
	"BHD": {"BHD", 3, "BHD", false, false, false},

	// Cryptocurrencies. Digits are what a budget needs, not the chain's full
	// precision (ETH has 18): amounts with more places are rejected as usual.
	"BTC":  {"BTC", 8, "BTC", false, true, false},
	"ETH":  {"ETH", 8, "ETH", false, true, false},
	"SOL":  {"SOL", 8, "SOL", false, true, false},
	"LTC":  {"LTC", 8, "LTC", false, true, false},
	"BNB":  {"BNB", 8, "BNB", false, true, false},
	"XRP":  {"XRP", 6, "XRP", false, true, false},
	"ADA":  {"ADA", 6, "ADA", false, true, false},
	"TRX":  {"TRX", 6, "TRX", false, true, false},
	"DOGE": {"DOGE", 8, "DOGE", false, true, false},
	"USDT": {"USDT", 6, "USDT", false, true, true},
	"USDC": {"USDC", 6, "USDC", false, true, true},
	"DAI":  {"DAI", 6, "DAI", false, true, true},
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
	out := make([]string, 0, len(currencies))
	for k := range currencies {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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
