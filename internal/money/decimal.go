package money

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
)

// MaxScale bounds the fractional digits accepted in a Decimal literal. It is
// large enough for a crypto rate quoted per unit of fiat (0.0000135 BTC per €)
// to keep ten significant digits.
const MaxScale = 18

// Decimal is an exact, immutable decimal number used for exchange rates,
// percentages and as the wire format of amounts. The zero value is 0.
// It serialises as a JSON string ("1.95") so no JSON parser can turn it into
// a float, and accepts either a string or a bare JSON number on input.
type Decimal struct{ r *big.Rat }

// ParseDecimal parses a plain decimal literal: optional sign, digits, optional
// fraction. Exponents, NaN, Inf, thousands separators and empty strings are
// rejected.
func ParseDecimal(s string) (Decimal, error) {
	t := strings.TrimSpace(s)
	body := strings.TrimPrefix(strings.TrimPrefix(t, "-"), "+")
	if body == "" {
		return Decimal{}, fmt.Errorf("invalid decimal %q", s)
	}
	intPart, frac, hasDot := strings.Cut(body, ".")
	if intPart == "" || (hasDot && frac == "") {
		return Decimal{}, fmt.Errorf("invalid decimal %q", s)
	}
	for _, part := range []string{intPart, frac} {
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				return Decimal{}, fmt.Errorf("invalid decimal %q", s)
			}
		}
	}
	if len(frac) > MaxScale {
		return Decimal{}, fmt.Errorf("decimal %q has more than %d fractional digits", s, MaxScale)
	}
	if len(intPart) > 30 {
		return Decimal{}, fmt.Errorf("decimal %q is too large", s)
	}
	r, ok := new(big.Rat).SetString(t)
	if !ok {
		return Decimal{}, fmt.Errorf("invalid decimal %q", s)
	}
	return Decimal{r}, nil
}

// MustDecimal is ParseDecimal for literals known at compile time.
func MustDecimal(s string) Decimal {
	d, err := ParseDecimal(s)
	if err != nil {
		panic(err)
	}
	return d
}

// RoundSignificant rounds r (half-even) to sig significant digits, or to
// MaxScale fractional digits if that is coarser. It turns a computed rate,
// such as 1/72565, into a Decimal that a document can store exactly.
func RoundSignificant(r *big.Rat, sig int) Decimal {
	if r.Sign() == 0 {
		return Decimal{new(big.Rat)}
	}
	abs := new(big.Rat).Abs(r)
	// Count integer digits: the smallest e with |r| < 10^e (e may be negative).
	e := 0
	for abs.Cmp(new(big.Rat).SetInt(pow10(max(e, 0)))) >= 0 {
		e++
	}
	for e > -MaxScale && new(big.Rat).Mul(abs, new(big.Rat).SetInt(pow10(-e+1))).Cmp(big.NewRat(1, 1)) < 0 {
		e--
	}
	scale := min(max(sig-e, 0), MaxScale)
	f := new(big.Rat).SetInt(pow10(scale))
	n := RoundInt(new(big.Rat).Mul(r, f), HalfEven)
	return Decimal{new(big.Rat).SetFrac(n, pow10(scale))}
}

// DecimalFromInt returns n as a Decimal.
func DecimalFromInt(n int64) Decimal { return Decimal{new(big.Rat).SetInt64(n)} }

// Rat returns a copy of the exact value.
func (d Decimal) Rat() *big.Rat {
	if d.r == nil {
		return new(big.Rat)
	}
	return new(big.Rat).Set(d.r)
}

func (d Decimal) Sign() int {
	if d.r == nil {
		return 0
	}
	return d.r.Sign()
}

// IsZero reports whether d is 0 (it also makes `json:",omitzero"` omit it).
func (d Decimal) IsZero() bool { return d.Sign() == 0 }

func (d Decimal) Cmp(o Decimal) int { return d.Rat().Cmp(o.Rat()) }

// Scale is the number of fractional digits needed to write d exactly.
// Parsed values always terminate within MaxScale digits; values derived by
// division may not, in which case MaxScale is returned (and String rounds).
func (d Decimal) Scale() int {
	den := d.Rat().Denom()
	for s := 0; s <= MaxScale; s++ {
		if new(big.Int).Mod(pow10(s), den).Sign() == 0 {
			return s
		}
	}
	return MaxScale
}

// String renders the shortest exact decimal form ("1.95", "90", "-0.5").
func (d Decimal) String() string {
	return d.Rat().FloatString(d.Scale())
}

func (d Decimal) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

func (d *Decimal) UnmarshalJSON(b []byte) error {
	var s string
	if len(b) > 0 && b[0] == '"' {
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
	} else {
		s = string(b) // bare JSON number: parse its literal text exactly
	}
	v, err := ParseDecimal(s)
	if err != nil {
		return err
	}
	*d = v
	return nil
}

func pow10(n int) *big.Int { return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil) }
