package money

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strings"
)

// RoundingMode says what to do with a fractional minor unit.
type RoundingMode int

const (
	// HalfEven (banker's rounding) is unbiased over many operations; use it for estimates.
	HalfEven RoundingMode = iota
	// Floor rounds toward -inf; use it for "how much can I afford with X".
	Floor
	// Ceil rounds toward +inf; use it for "how much must I pay to cover X".
	Ceil
)

// ArithmeticError is panicked by Money operations on overflow or currency
// mismatch. Both indicate a bug or absurd input; callers that process
// untrusted budgets convert it to an error with Recover.
type ArithmeticError struct{ Msg string }

func (e *ArithmeticError) Error() string { return "money: " + e.Msg }

// Recover turns an ArithmeticError panic into *err. Use as
// `defer money.Recover(&err)`. Other panics are re-raised.
func Recover(err *error) {
	if r := recover(); r != nil {
		if ae, ok := r.(*ArithmeticError); ok {
			*err = ae
			return
		}
		panic(r)
	}
}

// Money is an exact amount: a count of minor units in a currency.
type Money struct {
	minor int64
	cur   Currency
}

// New returns minor units of c (e.g. New(1234, EUR) is €12.34).
func New(minor int64, c Currency) Money { return Money{minor, c} }

// Zero returns 0 in c.
func Zero(c Currency) Money { return Money{0, c} }

// FromDecimal converts an exact decimal of major units. It fails if the value
// has more fractional digits than the currency allows (€1.234) or overflows.
func FromDecimal(d Decimal, c Currency) (Money, error) {
	scaled := new(big.Rat).Mul(d.Rat(), new(big.Rat).SetInt(pow10(c.Digits)))
	if !scaled.IsInt() {
		return Money{}, fmt.Errorf("%s allows at most %d decimal places, got %s", c.Code, c.Digits, d)
	}
	if !scaled.Num().IsInt64() {
		return Money{}, fmt.Errorf("amount %s %s is out of range", d, c.Code)
	}
	return Money{scaled.Num().Int64(), c}, nil
}

// Parse is FromDecimal(ParseDecimal(s)).
func Parse(s string, c Currency) (Money, error) {
	d, err := ParseDecimal(s)
	if err != nil {
		return Money{}, err
	}
	return FromDecimal(d, c)
}

// FromRat rounds an exact number of major units to the currency's minor unit.
func FromRat(major *big.Rat, c Currency, mode RoundingMode) Money {
	scaled := new(big.Rat).Mul(major, new(big.Rat).SetInt(pow10(c.Digits)))
	i := roundRat(scaled, mode)
	if !i.IsInt64() {
		panic(&ArithmeticError{"amount out of range in " + c.Code})
	}
	return Money{i.Int64(), c}
}

func (m Money) Minor() int64       { return m.minor }
func (m Money) Currency() Currency { return m.cur }
func (m Money) Sign() int {
	switch {
	case m.minor > 0:
		return 1
	case m.minor < 0:
		return -1
	}
	return 0
}
func (m Money) IsZero() bool     { return m.minor == 0 }
func (m Money) IsPositive() bool { return m.minor > 0 }
func (m Money) IsNegative() bool { return m.minor < 0 }

// Rat returns the exact value in major units.
func (m Money) Rat() *big.Rat {
	return new(big.Rat).SetFrac(big.NewInt(m.minor), pow10(m.cur.Digits))
}

// Decimal returns the exact value in major units.
func (m Money) Decimal() Decimal { return Decimal{m.Rat()} }

func (m Money) same(o Money) {
	if m.cur.Code != o.cur.Code {
		panic(&ArithmeticError{fmt.Sprintf("currency mismatch: %s vs %s", m.cur.Code, o.cur.Code)})
	}
}

func (m Money) Add(o Money) Money {
	m.same(o)
	s := m.minor + o.minor
	if (s > m.minor) != (o.minor > 0) {
		panic(&ArithmeticError{"overflow in " + m.cur.Code})
	}
	return Money{s, m.cur}
}

func (m Money) Sub(o Money) Money {
	if o.minor == math.MinInt64 {
		panic(&ArithmeticError{"overflow in " + m.cur.Code})
	}
	return m.Add(Money{-o.minor, o.cur})
}

func (m Money) Neg() Money { return Zero(m.cur).Sub(m) }

// Cmp compares two amounts of the same currency.
func (m Money) Cmp(o Money) int {
	m.same(o)
	switch {
	case m.minor < o.minor:
		return -1
	case m.minor > o.minor:
		return 1
	}
	return 0
}

func Min(a, b Money) Money {
	if a.Cmp(b) <= 0 {
		return a
	}
	return b
}

func Max(a, b Money) Money {
	if a.Cmp(b) >= 0 {
		return a
	}
	return b
}

// Mul multiplies by an exact factor and rounds to the minor unit.
func (m Money) Mul(f *big.Rat, mode RoundingMode) Money {
	return FromRat(new(big.Rat).Mul(m.Rat(), f), m.cur, mode)
}

// Percent returns pct% of m, rounded.
func (m Money) Percent(pct Decimal, mode RoundingMode) Money {
	return m.Mul(new(big.Rat).Quo(pct.Rat(), big.NewRat(100, 1)), mode)
}

// Sum adds amounts; all must be in c.
func Sum(c Currency, ms ...Money) Money {
	t := Zero(c)
	for _, m := range ms {
		t = t.Add(m)
	}
	return t
}

// String is the exact plain decimal ("-1234.50"), stable for machines.
func (m Money) String() string { return m.Rat().FloatString(m.cur.Digits) }

// Format renders for humans with grouping and symbol: "€1,234.50", "12,345.00 CHF".
func (m Money) Format() string { return m.format(m.cur.Digits, m.Rat()) }

// FormatWhole renders rounded to whole major units: "€1,235", "12,345 CHF".
func (m Money) FormatWhole() string {
	return m.format(0, new(big.Rat).SetInt(roundRat(m.Rat(), HalfEven)))
}

// Display is the short form UIs show: FormatWhole for fiat; for crypto, where
// whole units hide everything (0.015 BTC), the exact amount without trailing
// zeros ("0.015 BTC", "1,200 USDT").
func (m Money) Display() string {
	if !m.cur.Crypto {
		return m.FormatWhole()
	}
	d := m.Decimal()
	return m.format(d.Scale(), d.Rat())
}

func (m Money) format(digits int, v *big.Rat) string {
	neg := v.Sign() < 0
	s := new(big.Rat).Abs(v).FloatString(digits)
	intPart, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	for i, ch := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(ch)
	}
	num := b.String()
	if frac != "" {
		num += "." + frac
	}
	sign := ""
	if neg && strings.Trim(num, "0.,") != "" {
		sign = "−"
	}
	if m.cur.SymbolFirst {
		return sign + m.cur.Symbol + num
	}
	return sign + num + " " + m.cur.Symbol
}

type moneyJSON struct {
	Amount   Decimal `json:"amount"`
	Currency string  `json:"currency"`
	// Display is Money.Display, for UIs; ignored on input.
	Display string `json:"display,omitempty"`
}

// MarshalJSON emits {"amount":"1234.56","currency":"EUR","display":"€1,235"}.
// amount is exact; display is rounded for humans.
func (m Money) MarshalJSON() ([]byte, error) {
	if m.cur.Code == "" {
		return []byte("null"), nil
	}
	return json.Marshal(moneyJSON{
		Amount:   m.Decimal(),
		Currency: m.cur.Code,
		Display:  m.Display(),
	})
}

func (m *Money) UnmarshalJSON(b []byte) error {
	var j moneyJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	c, err := Lookup(j.Currency)
	if err != nil {
		return err
	}
	v, err := FromDecimal(j.Amount, c)
	if err != nil {
		return err
	}
	*m = v
	return nil
}

// roundRat rounds r to an integer using mode.
func roundRat(r *big.Rat, mode RoundingMode) *big.Int {
	num, den := r.Num(), r.Denom()                        // den > 0
	q, rem := new(big.Int).QuoRem(num, den, new(big.Int)) // truncates toward zero
	if rem.Sign() == 0 {
		return q
	}
	// floor: q is too high by one when the remainder is negative.
	floor := new(big.Int).Set(q)
	if rem.Sign() < 0 {
		floor.Sub(floor, big.NewInt(1))
	}
	switch mode {
	case Floor:
		return floor
	case Ceil:
		return floor.Add(floor, big.NewInt(1))
	}
	// HalfEven: compare the fractional part (r - floor) against 1/2.
	frac := new(big.Rat).Sub(r, new(big.Rat).SetInt(floor))
	switch frac.Cmp(big.NewRat(1, 2)) {
	case -1:
		return floor
	case 1:
		return floor.Add(floor, big.NewInt(1))
	}
	if floor.Bit(0) == 0 {
		return floor
	}
	return floor.Add(floor, big.NewInt(1))
}

// RoundInt rounds an exact rational to an integer using mode.
func RoundInt(r *big.Rat, mode RoundingMode) *big.Int { return roundRat(r, mode) }
