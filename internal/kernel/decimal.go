package kernel

import (
	"fmt"

	"github.com/cockroachdb/apd/v3"
)

// decimalContext is decimal128-like: 34 significant digits.
var decimalContext = apd.BaseContext.WithPrecision(34)

// Decimal is an exact decimal number, used for money (spec 3.2).
// The zero value is 0.
type Decimal struct {
	d apd.Decimal
}

// ParseDecimal parses a decimal string such as "-12.50".
func ParseDecimal(s string) (Decimal, error) {
	d, _, err := apd.NewFromString(s)
	if err != nil || d.Form != apd.Finite {
		return Decimal{}, fmt.Errorf("invalid decimal %q", s)
	}
	return Decimal{d: *d}, nil
}

// MustDecimal parses s and panics on error; for tests.
func MustDecimal(s string) Decimal {
	d, err := ParseDecimal(s)
	if err != nil {
		panic(err)
	}
	return d
}

// DecimalFromInt64 converts an integer.
func DecimalFromInt64(i int64) Decimal {
	var d Decimal
	d.d.SetInt64(i)
	return d
}

func (d Decimal) String() string { return d.d.Text('f') }

// Cmp compares d and o: -1, 0, or 1.
func (d Decimal) Cmp(o Decimal) int { return d.d.Cmp(&o.d) }

func (d Decimal) op(o Decimal, f func(res, x, y *apd.Decimal) (apd.Condition, error)) (Decimal, error) {
	var r Decimal
	if _, err := f(&r.d, &d.d, &o.d); err != nil {
		return Decimal{}, err
	}
	return r, nil
}

// Add returns d + o.
func (d Decimal) Add(o Decimal) (Decimal, error) { return d.op(o, decimalContext.Add) }

// Sub returns d - o.
func (d Decimal) Sub(o Decimal) (Decimal, error) { return d.op(o, decimalContext.Sub) }

// Mul returns d * o.
func (d Decimal) Mul(o Decimal) (Decimal, error) { return d.op(o, decimalContext.Mul) }

// Quo returns d / o; division by zero is an error.
func (d Decimal) Quo(o Decimal) (Decimal, error) {
	if o.d.IsZero() {
		return Decimal{}, fmt.Errorf("decimal division by zero")
	}
	return d.op(o, decimalContext.Quo)
}

// Round returns d with places decimal places, rounding half away from zero
// (2.345 → 2.35, -2.345 → -2.35); fewer digits are padded (2.5 → 2.50).
func (d Decimal) Round(places int32) (Decimal, error) {
	return d.quantize(-places, apd.RoundHalfUp)
}

// Floor returns the greatest integer not above d.
func (d Decimal) Floor() (Decimal, error) { return d.quantize(0, apd.RoundFloor) }

// Ceil returns the least integer not below d.
func (d Decimal) Ceil() (Decimal, error) { return d.quantize(0, apd.RoundCeiling) }

// Abs returns |d|.
func (d Decimal) Abs() Decimal {
	var r Decimal
	r.d.Abs(&d.d)
	return r
}

func (d Decimal) quantize(exp int32, rounding apd.Rounder) (Decimal, error) {
	ctx := *decimalContext
	ctx.Rounding = rounding
	var r Decimal
	if _, err := ctx.Quantize(&r.d, &d.d, exp); err != nil {
		return Decimal{}, err
	}
	if r.d.IsZero() {
		r.d.Negative = false // no "-0.00"
	}
	return r, nil
}

// MarshalText implements encoding.TextMarshaler (wire format is a string).
func (d Decimal) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Decimal) UnmarshalText(b []byte) error {
	p, err := ParseDecimal(string(b))
	if err != nil {
		return err
	}
	*d = p
	return nil
}
