package money

import (
	"math"
	"math/big"
	"strings"
	"testing"
)

func eq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

func TestComparisonsAndSums(t *testing.T) {
	a, b := New(150, EUR), New(-25, EUR)
	eq(t, "a.Cmp(b)", a.Cmp(b), 1)
	eq(t, "b.Cmp(a)", b.Cmp(a), -1)
	eq(t, "a.Cmp(a)", a.Cmp(a), 0)
	eq(t, "Min(a, b)", Min(a, b), b)
	eq(t, "Max(a, b)", Max(a, b), a)
	if got := Sum(EUR, a, b, New(1, EUR)); got.Minor() != 126 {
		t.Fatalf("Sum = %d", got.Minor())
	}
	eq(t, "Zero(EUR).IsZero()", Zero(EUR).IsZero(), true)
	eq(t, "a.Sign()", a.Sign(), 1)
	eq(t, "b.Sign()", b.Sign(), -1)
	eq(t, "b.IsNegative()", b.IsNegative(), true)
	eq(t, "a.IsPositive()", a.IsPositive(), true)
	eq(t, "a.Neg().Minor()", a.Neg().Minor(), -150)
	eq(t, "a.Currency().Code", a.Currency().Code, "EUR")
	eq(t, "a.Decimal().String()", a.Decimal().String(), "1.5")
	var err error
	func() { defer Recover(&err); New(math.MinInt64, EUR).Neg() }()
	if err == nil {
		t.Fatal("Neg(MinInt64) must overflow")
	}
	if err.Error() == "" {
		t.Fatal("Neg(MinInt64) overflow error has no message")
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
	eq(t, "DecimalFromInt(-7)", DecimalFromInt(-7).String(), "-7")
	eq(t, "zero Decimal sign", (Decimal{}).Sign(), 0)
	eq(t, "zero Decimal string", (Decimal{}).String(), "0")
	eq(t, "1.50 vs 1.5", MustDecimal("1.50").Cmp(MustDecimal("1.5")), 0)
	eq(t, "2 vs 1.99", MustDecimal("2").Cmp(MustDecimal("1.99")), 1)
	third := Decimal{big.NewRat(1, 3)} // non-terminating: rendered at MaxScale, never looping
	eq(t, "1/3 scale", third.Scale(), MaxScale)
	eq(t, "1/3 string", third.String(), "0."+strings.Repeat("3", MaxScale))
	eq(t, "RoundInt(5/2)", RoundInt(big.NewRat(5, 2), HalfEven).Int64(), 2)
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
		c := MustLookup(codes[i])
		eq(t, codes[i]+" code", c.Code, codes[i])
		eq(t, codes[i]+" string", c.String(), codes[i])
		if c.Digits < 0 {
			t.Fatalf("%s has negative digits %d", codes[i], c.Digits)
		}
		if c.Digits > 3 && !c.Crypto {
			t.Fatalf("%s is not crypto but has %d digits", codes[i], c.Digits)
		}
		if c.Digits > 9 {
			t.Fatalf("%s has %d digits, more than 9", codes[i], c.Digits)
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
		if err != nil {
			t.Fatalf("%q -> %s does not parse back: %v", s, d, err)
		}
		if back.Cmp(d) != 0 {
			t.Fatalf("%q -> %s does not round-trip to %s", s, d, back)
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
		if fl.Cmp(r) > 0 {
			t.Fatalf("%s: floor %s is above it", r, fl)
		}
		if ce.Cmp(r) < 0 {
			t.Fatalf("%s: ceil %s is below it", r, ce)
		}
		if new(big.Rat).Sub(ce, fl).Cmp(one) > 0 {
			t.Fatalf("%s: floor %s and ceil %s are more than 1 apart", r, fl, ce)
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
	eq(t, "BTC crypto", btc.Crypto, true)
	eq(t, "BTC stable", btc.Stable, false)
	eq(t, "USDT stable", MustLookup("USDT").Stable, true)
	eq(t, "EUR crypto", MustLookup("EUR").Crypto, false)
	m, err := Parse("0.015", btc)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "minor", m.Minor(), 1_500_000)
	eq(t, "display", m.Display(), "0.015 BTC")
	eq(t, "format", m.Format(), "0.01500000 BTC")
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
	if len(codes) == 0 {
		t.Fatal("no crypto codes")
	}
	eq(t, "first crypto code", codes[0], "ADA")
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
