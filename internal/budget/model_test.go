package budget

import "testing"

func TestGetCurrencyRate(t *testing.T) {
	b := blank()
	b.Currencies = []CurrencyRate{
		{Code: "USD", PerBase: d("1.08")},
		{Code: "CHF", PerBase: d("0.94")},
	}

	chf, found := b.GetCurrencyRate("CHF")
	_, foundJPY := b.GetCurrencyRate("JPY")

	eq(t, "CHF found", found, true)
	eq(t, "CHF per base", chf.PerBase.String(), "0.94")
	eq(t, "JPY found", foundJPY, false)
}
