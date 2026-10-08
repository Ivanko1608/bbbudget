package money

import (
	"encoding/json"
	"math"
	"math/big"
	"testing"
)

var (
	EUR = MustLookup("EUR")
	CHF = MustLookup("CHF")
	JPY = MustLookup("JPY")
	KWD = MustLookup("KWD")
)

func TestParseDecimal(t *testing.T) {
	ok := map[string]string{
		"0": "0", "90": "90", "1.95": "1.95", "-0.5": "-0.5", "+3": "3", " 12.50 ": "12.5",
		"0.000000000001": "0.000000000001", "007": "7",
	}
	for in, want := range ok {
		d, err := ParseDecimal(in)
		if err != nil {
			t.Fatalf("ParseDecimal(%q): %v", in, err)
		}
		if d.String() != want {
			t.Errorf("ParseDecimal(%q) = %s, want %s", in, d, want)
		}
	}
	for _, bad := range []string{"", "-", ".", ".5", "5.", "1e3", "NaN", "Inf", "1,000", "1.2.3", "0x10", "1/3", "0.0000000000000000001", "--1"} {
		if _, err := ParseDecimal(bad); err == nil {
			t.Errorf("ParseDecimal(%q) should fail", bad)
		}
	}
}

func TestDecimalJSON(t *testing.T) {
	var v struct{ A, B Decimal }
	if err := json.Unmarshal([]byte(`{"A":"1.10","B":0.1}`), &v); err != nil {
		t.Fatal(err)
	}
	// 0.1 as a bare JSON number must stay exactly 1/10, not 0.1000000000000000055...
	if v.B.Rat().Cmp(big.NewRat(1, 10)) != 0 {
		t.Fatalf("bare number lost precision: %s", v.B.Rat())
	}
	b, _ := json.Marshal(v)
	if string(b) != `{"A":"1.1","B":"0.1"}` {
		t.Fatalf("got %s", b)
	}
	if err := json.Unmarshal([]byte(`{"A":1e5}`), &v); err == nil {
		t.Fatal("exponent should be rejected")
	}
}

func TestFromDecimalRejectsExcessPrecision(t *testing.T) {
	if _, err := Parse("1.234", EUR); err == nil {
		t.Fatal("€1.234 should be rejected")
	}
	if _, err := Parse("1.5", JPY); err == nil {
		t.Fatal("¥1.5 should be rejected")
	}
	m, err := Parse("1.234", KWD)
	if err != nil || m.Minor() != 1234 {
		t.Fatalf("KWD 1.234 = %v, %v", m, err)
	}
	if _, err := Parse("100000000000000000000", EUR); err == nil {
		t.Fatal("out of range should be rejected")
	}
}

func TestRoundingModes(t *testing.T) {
	cases := []struct {
		num, den          int64
		even, floor, ceil int64
	}{
		{5, 2, 2, 2, 3},     // 2.5
		{7, 2, 4, 3, 4},     // 3.5
		{-5, 2, -2, -3, -2}, // -2.5
		{-7, 2, -4, -4, -3}, // -3.5
		{10, 3, 3, 3, 4},    // 3.33
		{-10, 3, -3, -4, -3},
		{20, 3, 7, 6, 7}, // 6.67
		{4, 1, 4, 4, 4},
		{0, 1, 0, 0, 0},
	}
	for _, c := range cases {
		r := big.NewRat(c.num, c.den)
		got := []int64{roundRat(r, HalfEven).Int64(), roundRat(r, Floor).Int64(), roundRat(r, Ceil).Int64()}
		want := []int64{c.even, c.floor, c.ceil}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("%d/%d mode %d: got %d want %d", c.num, c.den, i, got[i], want[i])
			}
		}
	}
}

func TestArithmeticPanicsAreRecoverable(t *testing.T) {
	run := func(f func()) (err error) {
		defer Recover(&err)
		f()
		return nil
	}
	if err := run(func() { New(math.MaxInt64, EUR).Add(New(1, EUR)) }); err == nil {
		t.Error("overflow not detected")
	}
	if err := run(func() { New(math.MinInt64, EUR).Sub(New(1, EUR)) }); err == nil {
		t.Error("underflow not detected")
	}
	if err := run(func() { New(1, EUR).Sub(New(math.MinInt64, EUR)) }); err == nil {
		t.Error("negation overflow not detected")
	}
	if err := run(func() { New(1, EUR).Add(New(1, CHF)) }); err == nil {
		t.Error("currency mismatch not detected")
	}
	if err := run(func() { New(math.MaxInt64, EUR).Mul(big.NewRat(2, 1), HalfEven) }); err == nil {
		t.Error("Mul overflow not detected")
	}
}

func TestPercentAndMul(t *testing.T) {
	m := New(12345_00, CHF)
	if got := m.Percent(MustDecimal("5"), HalfEven); got.Minor() != 617_25 {
		t.Fatalf("5%% of 12345 = %s", got)
	}
	// 0.125 cents rounds half-even to 0, 0.375 -> 0, 0.5 -> 0, 1.5 -> 2
	if got := New(1, EUR).Mul(big.NewRat(1, 2), HalfEven); got.Minor() != 0 {
		t.Fatalf("half a cent = %d", got.Minor())
	}
	if got := New(3, EUR).Mul(big.NewRat(1, 2), HalfEven); got.Minor() != 2 {
		t.Fatalf("1.5 cents = %d", got.Minor())
	}
}

func TestFormat(t *testing.T) {
	cases := map[string]string{
		New(123456, EUR).Format():       "€1,234.56",
		New(-123456, EUR).Format():      "−€1,234.56",
		New(123456, EUR).FormatWhole():  "€1,235",
		New(1234500, CHF).FormatWhole(): "12,345 CHF",
		New(1000000, JPY).Format():      "¥1,000,000",
		New(-40, EUR).FormatWhole():     "€0",
		New(12, EUR).String():           "0.12",
		New(-5, EUR).String():           "-0.05",
		New(1234, KWD).String():         "1.234",
		New(250, EUR).FormatWhole():     "€2",
		New(350, EUR).FormatWhole():     "€4",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
}

func TestMoneyJSONRoundTrip(t *testing.T) {
	m := New(-123456, EUR)
	b, err := json.Marshal(m)
	if err != nil || string(b) != `{"amount":"-1234.56","currency":"EUR","display":"−€1,235"}` {
		t.Fatalf("%s %v", b, err)
	}
	var back Money
	if err := json.Unmarshal(b, &back); err != nil || back != m {
		t.Fatalf("%v %v", back, err)
	}
	if err := json.Unmarshal([]byte(`{"amount":"1.001","currency":"EUR"}`), &back); err == nil {
		t.Fatal("excess precision accepted")
	}
}
