package budget

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// cryptoBudget: €2,000 salary, 0.015 BTC a month at 1 BTC = €100,000 with a
// 1% cash-out loss, and €2,500 of rent.
func cryptoBudget() Budget {
	b := blank()
	b.Currencies = []CurrencyRate{{
		Code:           "BTC",
		PerBase:        d("0.00001"),
		TransferFeePct: d("0"),
		CashOutFeePct:  d("1"),
		Source:         RateSourceAuto,
	}}
	b.Items = []Item{
		{ID: "salary", Name: "Salary", Amount: d("2000"), Currency: "EUR",
			Category: "income", Schedule: Monthly()},
		{ID: "mining", Name: "Mining payout", Amount: d("0.015"), Currency: "BTC",
			Category: "income", Schedule: Monthly()},
		{ID: "rent", Name: "Rent", Amount: d("2500"), Currency: "EUR",
			Category: "living", Schedule: Monthly()},
	}
	return b
}

func TestCryptoIncomeAfterCashOutLoss(t *testing.T) {
	r := mustReport(t, cryptoBudget())
	m := r.Months[0]
	// 0.015 BTC = €1,500 at the mid rate, less 1% = €1,485.
	eqMoney(t, "income", m.Income, "3485.00")
	eqMoney(t, "left", m.Left, "985.00")
}

func TestCryptoInsights(t *testing.T) {
	withNode := func() Budget {
		b := cryptoBudget()
		b.Items = append(b.Items, Item{ID: "node", Name: "Node hosting", Amount: d("0.001"),
			Currency: "BTC", Category: "living", Schedule: Monthly()})
		return b
	}
	shortRent := func() Budget {
		b := cryptoBudget()
		// €185 left; a €445.50 fall leaves €260.50 short (shown half-even)
		b.Items[2].Amount = d("3300")
		return b
	}
	cases := []struct {
		name   string
		budget func() Budget
		id     string
		tone   Tone
		texts  []string
	}{
		{
			name:   "income share and a 30% fall",
			budget: cryptoBudget,
			id:     "crypto_income",
			tone:   ToneInfo,
			// 1,485 is 43% of 3,485; a 30% fall costs €445.50 → €446;
			// €985 − €445.50 = €539.50 → €540 shown.
			texts: []string{"**43%**", "BTC", "**€446**"},
		},
		{
			name:   "cash-out loss",
			budget: cryptoBudget,
			id:     "cash_out_loss_btc",
			tone:   ToneInfo,
			texts:  []string{"0.015 BTC", "**€15**", "€180 a year"},
		},
		{
			name:   "small rates are not rounded to 0.00",
			budget: withNode,
			id:     "fx_risk_btc",
			tone:   ToneInfo,
			texts:  []string{"0.000009 per EUR", "0.000011"},
		},
		{
			name:   "a fall that leaves the month short warns",
			budget: shortRent,
			id:     "crypto_income",
			tone:   ToneWarn,
			texts:  []string{"**€260 short**"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := insight(mustReport(t, c.budget()), c.id)
			if in == nil {
				t.Fatalf("no %s insight", c.id)
			}
			eq(t, "tone", in.Tone, c.tone)
			for _, text := range c.texts {
				contains(t, c.id, in.Text, text)
			}
		})
	}
}

func TestCryptoValidation(t *testing.T) {
	b := cryptoBudget()
	b.BaseCurrency = "BTC"
	b.Currencies = nil
	_, err := Compile(b)
	errorContains(t, "crypto base currency", err, "cryptocurrency")

	b = cryptoBudget()
	b.Items[1].Amount = d("20000000") // 2×10^15 satoshi: over MaxAmountMinor
	b.Currencies[0].CashOutFeePct = d("100")
	b.Currencies[0].Source = "google"
	_, err = Compile(b)
	for _, want := range []string{"items[1].amount", "cash_out_fee_pct", "currencies[0].source"} {
		errorContains(t, "issue at "+want, err, want)
	}
}

func TestTypedRateBecomesManual(t *testing.T) {
	b := cryptoBudget()
	b.Currencies[0].RateDate, b.Currencies[0].RateFrom = "2026-10-07", "ecb"
	nb, err := Apply(b, NewOp("set_currency", "btc", CurrencyPatch{PerBase: decp("0.000012")}))
	if err != nil {
		t.Fatal(err)
	}
	typed := nb.Currencies[0]
	eq(t, "typed source", typed.Source, RateSourceManual)
	eq(t, "typed rate date", typed.RateDate, "")
	eq(t, "typed rate from", typed.RateFrom, "")
	eq(t, "typed rate", typed.PerBase.String(), "0.000012")

	// A fetched rate names its source and keeps it.
	fetched := CurrencyPatch{
		PerBase:  decp("0.000013"),
		Source:   rateSourcep(RateSourceAuto),
		RateDate: strp("2026-10-08"),
	}
	nb, err = Apply(b, NewOp("set_currency", "BTC", fetched))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "fetched source", nb.Currencies[0].Source, RateSourceAuto)
	eq(t, "fetched rate date", nb.Currencies[0].RateDate, "2026-10-08")
}

func TestReportCurrencies(t *testing.T) {
	b := cryptoBudget()
	b.Currencies = append(b.Currencies,
		CurrencyRate{Code: "USD", PerBase: d("1.1186"), TransferFeePct: d("0")},
		CurrencyRate{Code: "KRW", PerBase: d("1642.5"), TransferFeePct: d("0")})
	r := mustReport(t, b)
	if len(r.Currencies) != 3 {
		t.Fatalf("%+v", r.Currencies)
	}
	for i, want := range []struct{ code, units, value string }{
		{"BTC", "1", "100000.00"}, // 1 / 0.00001
		{"USD", "1", "0.89"},      // 1 / 1.1186 = 0.8939…
		{"KRW", "1000", "0.61"},   // 1 KRW would show as €0.00: 1,000 / 1642.5 = 0.6088…
	} {
		c := r.Currencies[i]
		eq(t, "code", c.Code, want.code)
		eq(t, want.code+" units", c.Units.String(), want.units)
		eq(t, want.code+" value", c.BaseValue.String(), want.value)
		eq(t, want.code+" crypto", c.Crypto, want.code == "BTC")
	}
	if r := mustReport(t, blank()); r.Currencies == nil {
		t.Error("currencies must be [] rather than null in JSON")
	}
}

func TestCryptoAmountsOnTheWire(t *testing.T) {
	r := mustReport(t, cryptoBudget())
	raw, err := json.Marshal(r.Items[1].Amount)
	if err != nil {
		t.Fatal(err)
	}
	// Exact amount, and a display that doesn't round 0.015 BTC to "0".
	if string(raw) != `{"amount":"0.015","currency":"BTC","display":"0.015 BTC"}` {
		t.Fatal(string(raw))
	}
}

func TestCryptoFromYAML(t *testing.T) {
	b, err := ParseDocument([]byte(`
name: Y
base_currency: EUR
start_month: 2026-10
currencies:
  - {code: BTC, source: auto, per_base: 0.0000135211580123, cash_out_fee_pct: 1.5}
  - {code: USDT, per_base: 1.12122692}
categories: [{label: Income, kind: income}]
items:
  - {name: Payout, amount: 0.00012345, currency: BTC, category: income}
  - {name: Stable, amount: 250.123456, currency: USDT, category: income}
`), "yaml")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "BTC rate", b.Currencies[0].PerBase.String(), "0.0000135211580123")
	eq(t, "payout", b.Items[0].Amount.String(), "0.00012345")
	eq(t, "stable", b.Items[1].Amount.String(), "250.123456")
	eq(t, "omitted source", b.Currencies[1].Source, "")
	eq(t, "omitted source means manual", b.Currencies[1].IsManual(), true)
	eq(t, "omitted cash-out loss means 0", b.Currencies[1].CashOutFeePct.IsZero(), true)
	if _, err := Compile(b); err != nil {
		t.Fatal(err)
	}
	// Omitted optional fields stay omitted when written back.
	raw, _ := json.Marshal(b.Currencies[1])
	notContains(t, "written currency", string(raw), "cash_out_fee_pct")
	notContains(t, "written currency", string(raw), "source")
}

func TestStablecoinIncomeIsNotCryptoRisk(t *testing.T) {
	b := blank()
	b.Currencies = []CurrencyRate{{Code: "USDT", PerBase: d("1.1"), TransferFeePct: d("0")}}
	b.Items = []Item{{ID: "pay", Name: "Pay", Amount: d("1100"), Currency: "USDT",
		Category: "income", Schedule: Monthly()}}
	for _, in := range mustReport(t, b).Insights {
		if in.ID == "crypto_income" {
			t.Fatalf("stablecoin flagged as volatile: %s", in.Text)
		}
	}
}

func TestManualRateHasNoFetchMetadata(t *testing.T) {
	b := cryptoBudget()
	b.Currencies[0].RateDate, b.Currencies[0].RateFrom = "2026-10-01", "ecb"
	for _, p := range []CurrencyPatch{
		{Source: rateSourcep(RateSourceManual)},
		{PerBase: decp("0.00002"), Source: rateSourcep(RateSourceManual)},
		{PerBase: decp("0.00002")},
	} {
		nb, err := Apply(b, NewOp("set_currency", "BTC", p))
		if err != nil {
			t.Fatal(err)
		}
		c := nb.Currencies[0]
		eq(t, fmt.Sprintf("%+v rate date", p), c.RateDate, "")
		eq(t, fmt.Sprintf("%+v rate from", p), c.RateFrom, "")
		eq(t, fmt.Sprintf("%+v is manual", p), c.IsManual(), true)
	}
}

// A budget with nothing in it is what every new user starts from, and the UI
// calls .map on these lists.
func TestEmptyReportHasNoNullLists(t *testing.T) {
	raw, err := json.Marshal(mustReport(t, blank()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), ":null") {
		t.Errorf("lists must be [] rather than null in JSON: %s", raw)
	}
}
