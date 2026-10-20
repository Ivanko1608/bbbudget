package money

import (
	"math"
	"math/big"
	"strings"
	"testing"
)

func TestComparisonsAndSums(t *testing.T) {
	a, b := New(150, EUR), New(-25, EUR)
	if a.Cmp(b) != 1 || b.Cmp(a) != -1 || a.Cmp(a) != 0 {
		t.Fatal("Cmp")
	}
	if Min(a, b) != b || Max(a, b) != a {
		t.Fatal("Min/Max")
	}
	if got := Sum(EUR, a, b, New(1, EUR)); got.Minor() != 126 {
		t.Fatalf("Sum = %d", got.Minor())
	}
	if !Zero(EUR).IsZero() || a.Sign() != 1 || b.Sign() != -1 || !b.IsNegative() || !a.IsPositive() || a.Neg().Minor() != -150 {
		t.Fatal("sign helpers")
	}
	if a.Currency().Code != "EUR" || a.Decimal().String() != "1.5" {
		t.Fatal("accessors")
	}
	var err error
	func() { defer Recover(&err); New(math.MinInt64, EUR).Neg() }()
	if err == nil || err.Error() == "" {
		t.Fatal("Neg(MinInt64) must overflow")
	}
	func() { defer Recover(&err); New(1, EUR).Cmp(New(1, CHF)) }()
	if err == nil {
		t.Fatal("cross-currency Cmp must fail")
	}
}

func TestRecoverRepanicsForeignPanics(t *testing.T) {
	defer func() {
		if r := recover(); r != "boom" {
			t.Fatalf("got %v", r)
		}
	}()
	var err error
	func() { defer Recover(&err); panic("boom") }()
}

func TestDecimalHelpers(t *testing.T) {
	if DecimalFromInt(-7).String() != "-7" || (Decimal{}).Sign() != 0 || (Decimal{}).String() != "0" {
		t.Fatal("zero/int decimals")
	}
	if MustDecimal("1.50").Cmp(MustDecimal("1.5")) != 0 || MustDecimal("2").Cmp(MustDecimal("1.99")) != 1 {
		t.Fatal("Cmp")
	}
	third := Decimal{big.NewRat(1, 3)} // non-terminating: rendered at MaxScale, never looping
	if third.Scale() != MaxScale || third.String() != "0."+strings.Repeat("3", MaxScale) {
		t.Fatalf("1/3 = %s", third)
	}
	if RoundInt(big.NewRat(5, 2), HalfEven).Int64() != 2 {
		t.Fatal("RoundInt")
	}
}

func TestCurrencies(t *testing.T) {
	if _, err := Lookup("XXX"); err == nil {
		t.Fatal("unknown currency accepted")
	}
	codes := Codes()
	for i := 1; i < len(codes); i++ {
		if codes[i-1] >= codes[i] {
			t.Fatal("Codes not sorted")
		}
		if c := MustLookup(codes[i]); c.Code != codes[i] || c.String() != codes[i] || c.Digits < 0 || (c.Digits > 3 && !c.Crypto) || c.Digits > 9 {
			t.Fatalf("bad table entry %+v", c)
		}
	}
	defer func() {
		if recover() == nil {
			t.Fatal("MustLookup should panic")
		}
	}()
	MustLookup("nope")
}

// Any accepted literal must round-trip exactly; nothing may panic.
func FuzzParseDecimal(f *testing.F) {
	for _, s := range []string{"0", "1.95", "-0.5", "1e3", "", ".", "999999999999999999999999999999.999999999999", "+0.000000000001"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		d, err := ParseDecimal(s)
		if err != nil {
			return
		}
		back, err := ParseDecimal(d.String())
		if err != nil || back.Cmp(d) != 0 {
			t.Fatalf("%q -> %s does not round-trip (%v)", s, d, err)
		}
		if m, err := FromDecimal(d, EUR); err == nil && m.Rat().Cmp(d.Rat()) != 0 {
			t.Fatalf("FromDecimal(%s) changed the value to %s", d, m)
		}
	})
}

// Rounding must land within one unit, on the correct side for each mode.
func FuzzRounding(f *testing.F) {
	f.Add(int64(5), int64(2))
	f.Add(int64(-7), int64(3))
	f.Fuzz(func(t *testing.T, num, den int64) {
		if den == 0 {
			return
		}
		r := big.NewRat(num, den)
		fl, ce, he := new(big.Rat).SetInt(RoundInt(r, Floor)), new(big.Rat).SetInt(RoundInt(r, Ceil)), new(big.Rat).SetInt(RoundInt(r, HalfEven))
		one := big.NewRat(1, 1)
		if fl.Cmp(r) > 0 || ce.Cmp(r) < 0 || new(big.Rat).Sub(ce, fl).Cmp(one) > 0 {
			t.Fatalf("%s: floor %s ceil %s", r, fl, ce)
		}
		if he.Cmp(fl) != 0 && he.Cmp(ce) != 0 {
			t.Fatalf("%s: half-even %s is neither floor nor ceil", r, he)
		}
		if d := new(big.Rat).Abs(new(big.Rat).Sub(he, r)); d.Cmp(big.NewRat(1, 2)) > 0 {
			t.Fatalf("%s: half-even %s is more than 1/2 away", r, he)
		}
	})
}

func TestRoundSignificant(t *testing.T) {
	for _, c := range []struct {
		r    *big.Rat
		sig  int
		want string
	}{
		{big.NewRat(1, 72565), 10, "0.00001378074829"},
		{big.NewRat(1, 3), 4, "0.3333"},
		{big.NewRat(2, 3), 4, "0.6667"},
		{big.NewRat(123456789, 1000), 4, "123457"},
		{big.NewRat(1186, 1000), 10, "1.186"},
		{big.NewRat(-5, 100), 3, "-0.05"},
		{big.NewRat(0, 1), 5, "0"},
		{new(big.Rat).SetFrac(big.NewInt(1), pow10(19)), 10, "0"}, // below MaxScale
	} {
		if got := RoundSignificant(c.r, c.sig).String(); got != c.want {
			t.Errorf("RoundSignificant(%s, %d) = %s, want %s", c.r, c.sig, got, c.want)
		}
	}
}

func TestCrypto(t *testing.T) {
	btc := MustLookup("BTC")
	if !btc.Crypto || btc.Stable || !MustLookup("USDT").Stable || MustLookup("EUR").Crypto {
		t.Fatal("crypto flags")
	}
	m, err := Parse("0.015", btc)
	if err != nil {
		t.Fatal(err)
	}
	if m.Minor() != 1_500_000 || m.Display() != "0.015 BTC" || m.Format() != "0.01500000 BTC" {
		t.Fatalf("got %d %q %q", m.Minor(), m.Display(), m.Format())
	}
	if _, err := Parse("0.000000001", btc); err == nil {
		t.Fatal("9 places of BTC accepted")
	}
	if d := New(120_000_000, MustLookup("USDT")).Display(); d != "120 USDT" {
		t.Fatal(d)
	}
	if d := New(123456, EUR).Display(); d != "€1,235" {
		t.Fatal(d)
	}
	codes := CryptoCodes()
	if len(codes) == 0 || codes[0] != "ADA" {
		t.Fatal(codes)
	}
}

func TestDecimalAbs(t *testing.T) {
	negative := MustDecimal("-1.5")

	got := negative.Abs()

	if got.String() != "1.5" {
		t.Fatalf("Abs = %s", got)
	}
	if negative.String() != "-1.5" {
		t.Fatalf("Abs changed its receiver: %s", negative)
	}
}
