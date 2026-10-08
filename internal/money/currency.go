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

// Currency is an ISO 4217 code with its number of minor-unit digits.
type Currency struct {
	Code   string
	Digits int
	Symbol string
	// SymbolFirst renders "€12" rather than "12 ₽".
	SymbolFirst bool
}

// Built-in ISO 4217 table. Extend here to support more currencies.
var currencies = map[string]Currency{
	"EUR": {"EUR", 2, "€", true},
	"USD": {"USD", 2, "$", true},
	"GBP": {"GBP", 2, "£", true},
	"CHF": {"CHF", 2, "CHF", false},
	"RUB": {"RUB", 2, "₽", false},
	"AZN": {"AZN", 2, "₼", false},
	"GEL": {"GEL", 2, "₾", false},
	"TRY": {"TRY", 2, "₺", false},
	"KZT": {"KZT", 2, "₸", false},
	"UAH": {"UAH", 2, "₴", false},
	"PLN": {"PLN", 2, "zł", false},
	"CZK": {"CZK", 2, "Kč", false},
	"SEK": {"SEK", 2, "kr", false},
	"NOK": {"NOK", 2, "kr", false},
	"DKK": {"DKK", 2, "kr", false},
	"RSD": {"RSD", 2, "RSD", false},
	"AMD": {"AMD", 2, "֏", false},
	"AED": {"AED", 2, "AED", false},
	"INR": {"INR", 2, "₹", true},
	"CNY": {"CNY", 2, "¥", true},
	"CAD": {"CAD", 2, "CA$", true},
	"AUD": {"AUD", 2, "A$", true},
	"JPY": {"JPY", 0, "¥", true},
	"KRW": {"KRW", 0, "₩", true},
	"KWD": {"KWD", 3, "KWD", false},
	"BHD": {"BHD", 3, "BHD", false},
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

func (c Currency) String() string { return c.Code }
