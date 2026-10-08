package money

import (
	"math"
	"math/big"
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
	if third.Scale() != MaxScale || third.String() != "0.333333333333" {
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
		if c := MustLookup(codes[i]); c.Code != codes[i] || c.String() != codes[i] || c.Digits < 0 || c.Digits > 3 {
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
