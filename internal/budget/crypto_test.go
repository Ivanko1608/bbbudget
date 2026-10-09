package budget

import (
	"encoding/json"
	"strings"
	"testing"
)

// cryptoBudget: €2,000 salary, 0.015 BTC a month at 1 BTC = €100,000 with a
// 1% cash-out loss, and €2,500 of rent.
func cryptoBudget() Budget {
	b := blank()
	b.Currencies = []CurrencyRate{{Code: "BTC", PerBase: d("0.00001"), TransferFeePct: d("0"), CashOutFeePct: d("1"), Source: SourceAuto}}
	b.Lines = []Line{
		{ID: "salary", Name: "Salary", Amount: d("2000"), Currency: "EUR", Category: "income", Schedule: Monthly()},
		{ID: "mining", Name: "Mining payout", Amount: d("0.015"), Currency: "BTC", Category: "income", Schedule: Monthly()},
		{ID: "rent", Name: "Rent", Amount: d("2500"), Currency: "EUR", Category: "living", Schedule: Monthly()},
	}
	return b
}

func TestCryptoIncomeAfterCashOutLoss(t *testing.T) {
	r := mustReport(t, cryptoBudget())
	m := r.Months[0]
	// 0.015 BTC = €1,500 at the mid rate, less 1% = €1,485.
	eq(t, "income", m.Income, "3485.00")
	eq(t, "left", m.Left, "985.00")

	var crypto, cashOut string
	for _, in := range r.Insights {
		switch in.ID {
		case "crypto_income":
			crypto = in.Text
		case "cash_out_loss_btc":
			cashOut = in.Text
		}
	}
	// 1,485 is 43% of 3,485; a 30% fall costs €445.50 → €446; €985 − €445.50 = €539.50 → €540 shown.
	if !strings.Contains(crypto, "**43%**") || !strings.Contains(crypto, "BTC") || !strings.Contains(crypto, "**€446**") {
		t.Errorf("crypto insight: %q", crypto)
	}
	if !strings.Contains(cashOut, "0.015 BTC") || !strings.Contains(cashOut, "**€15**") || !strings.Contains(cashOut, "€180 a year") {
		t.Errorf("cash-out insight: %q", cashOut)
	}
}

func TestCryptoValidation(t *testing.T) {
	b := cryptoBudget()
	b.BaseCurrency = "BTC"
	b.Currencies = nil
	if _, err := Compile(b); err == nil || !strings.Contains(err.Error(), "cryptocurrency") {
		t.Fatalf("crypto base currency: %v", err)
	}

	b = cryptoBudget()
	b.Lines[1].Amount = d("20000000") // 2×10^15 satoshi: over MaxAmountMinor
	b.Currencies[0].CashOutFeePct = d("100")
	b.Currencies[0].Source = "google"
	_, err := Compile(b)
	for _, want := range []string{"lines[1].amount", "cash_out_fee_pct", "currencies[0].source"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want an issue at %s, got %v", want, err)
		}
	}
}

func TestTypedRateBecomesManual(t *testing.T) {
	b := cryptoBudget()
	b.Currencies[0].RateDate, b.Currencies[0].RateFrom = "2026-10-07", "ecb"
	nb, err := Apply(b, NewOp("set_currency", "btc", CurrencyPatch{PerBase: decp("0.000012")}))
	if err != nil {
		t.Fatal(err)
	}
	if c := nb.Currencies[0]; c.Source != SourceManual || c.RateDate != "" || c.RateFrom != "" || c.PerBase.String() != "0.000012" {
		t.Fatalf("got %+v", c)
	}
	// A fetched rate names its source and keeps it.
	nb, err = Apply(b, NewOp("set_currency", "BTC", CurrencyPatch{PerBase: decp("0.000013"), Source: strp(SourceAuto), RateDate: strp("2026-10-08")}))
	if err != nil || nb.Currencies[0].Source != SourceAuto || nb.Currencies[0].RateDate != "2026-10-08" {
		t.Fatalf("got %+v, %v", nb.Currencies[0], err)
	}
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
		if c.Code != want.code || c.Units.String() != want.units || c.BaseValue.String() != want.value || c.Crypto != (want.code == "BTC") {
			t.Errorf("%s: %s units = %s, want %s = %s", c.Code, c.Units, c.BaseValue, want.units, want.value)
		}
	}
	if r := mustReport(t, blank()); r.Currencies == nil {
		t.Error("currencies must be [] rather than null in JSON")
	}
}

func TestCryptoAmountsOnTheWire(t *testing.T) {
	r := mustReport(t, cryptoBudget())
	raw, err := json.Marshal(r.Lines[1].Amount)
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
lines:
  - {name: Payout, amount: 0.00012345, currency: BTC, category: income}
  - {name: Stable, amount: 250.123456, currency: USDT, category: income}
`), "yaml")
	if err != nil {
		t.Fatal(err)
	}
	if b.Currencies[0].PerBase.String() != "0.0000135211580123" || b.Lines[0].Amount.String() != "0.00012345" || b.Lines[1].Amount.String() != "250.123456" {
		t.Fatalf("exact values lost: %+v", b)
	}
	if b.Currencies[1].Source != "" || !b.Currencies[1].IsManual() || !b.Currencies[1].CashOutFeePct.IsZero() {
		t.Fatal("omitted source means manual, omitted cash-out loss means 0")
	}
	if _, err := Compile(b); err != nil {
		t.Fatal(err)
	}
	// Omitted optional fields stay omitted when written back.
	raw, _ := json.Marshal(b.Currencies[1])
	if strings.Contains(string(raw), "cash_out_fee_pct") || strings.Contains(string(raw), "source") {
		t.Fatal(string(raw))
	}
}

func TestFXRiskShowsSmallRates(t *testing.T) {
	b := cryptoBudget()
	b.Lines = append(b.Lines, Line{ID: "node", Name: "Node hosting", Amount: d("0.001"), Currency: "BTC", Category: "living", Schedule: Monthly()})
	for _, in := range mustReport(t, b).Insights {
		if in.ID == "fx_risk_btc" {
			if !strings.Contains(in.Text, "0.000009 per EUR") || !strings.Contains(in.Text, "0.000011") {
				t.Fatalf("rates must not print as 0.00: %s", in.Text)
			}
			return
		}
	}
	t.Fatal("no fx_risk_btc insight")
}

func TestStablecoinIncomeIsNotCryptoRisk(t *testing.T) {
	b := blank()
	b.Currencies = []CurrencyRate{{Code: "USDT", PerBase: d("1.1"), TransferFeePct: d("0")}}
	b.Lines = []Line{{ID: "pay", Name: "Pay", Amount: d("1100"), Currency: "USDT", Category: "income", Schedule: Monthly()}}
	for _, in := range mustReport(t, b).Insights {
		if in.ID == "crypto_income" {
			t.Fatalf("stablecoin flagged as volatile: %s", in.Text)
		}
	}
}

func TestCryptoShortfallWarns(t *testing.T) {
	b := cryptoBudget()
	b.Lines[2].Amount = d("3300") // €185 left; a €445.50 fall leaves €260.50 short (shown half-even)
	for _, in := range mustReport(t, b).Insights {
		if in.ID == "crypto_income" {
			if in.Tone != ToneWarn || !strings.Contains(in.Text, "**€260 short**") {
				t.Fatalf("%s %s", in.Tone, in.Text)
			}
			return
		}
	}
	t.Fatal("no crypto_income insight")
}

func TestManualRateHasNoFetchMetadata(t *testing.T) {
	b := cryptoBudget()
	b.Currencies[0].RateDate, b.Currencies[0].RateFrom = "2026-10-01", "ecb"
	for _, p := range []CurrencyPatch{
		{Source: strp(SourceManual)},
		{PerBase: decp("0.00002"), Source: strp(SourceManual)},
		{PerBase: decp("0.00002")},
	} {
		nb, err := Apply(b, NewOp("set_currency", "BTC", p))
		if err != nil {
			t.Fatal(err)
		}
		if c := nb.Currencies[0]; c.RateDate != "" || c.RateFrom != "" || !c.IsManual() {
			t.Errorf("%+v left %+v", p, c)
		}
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
