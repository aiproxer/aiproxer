package metering

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

const (
	// MaxDecimalCoefficientDigits is the maximum number of significant base-10
	// digits retained in a canonical Decimal.
	MaxDecimalCoefficientDigits = 38
	// MaxDecimalScale is the maximum number of decimal places in a Decimal.
	MaxDecimalScale uint8 = 18
	// LedgerNanoScale is the scale of the integer major-currency ledger unit.
	LedgerNanoScale uint8 = 9
	// MaxDecimalLiteralBytes bounds work done by the scientific-notation parser.
	MaxDecimalLiteralBytes = 256
)

var (
	// ErrInvalidDecimal identifies malformed or non-canonical decimal input.
	ErrInvalidDecimal = errors.New("metering: invalid decimal")
	// ErrDecimalOverflow identifies a decimal that cannot be represented within
	// the bounded coefficient/scale contract or as checked ledger nanos.
	ErrDecimalOverflow = errors.New("metering: decimal overflow")
	// ErrDecimalPrecision identifies a value with more precision than the
	// destination contract can represent exactly.
	ErrDecimalPrecision = errors.New("metering: decimal precision unsupported")
)

// Decimal is a bounded exact base-10 value. Its value is
// Coefficient * 10^-Scale. Coefficient is a signed canonical base-10 integer;
// negative zero and insignificant trailing fractional zeroes are normalized
// away before a value is used in an identity or calculation.
type Decimal struct {
	Coefficient string `json:"coefficient"`
	Scale       uint8  `json:"scale"`
}

// Validate accepts only canonical Decimal values. Use Normalize for a value
// received from a permissive decoder or ParseDecimal for a textual value.
func (d Decimal) Validate() error {
	if d.Scale > MaxDecimalScale {
		return fmt.Errorf("%w: scale %d exceeds %d", ErrInvalidDecimal, d.Scale, MaxDecimalScale)
	}
	if d.Coefficient == "" {
		return fmt.Errorf("%w: coefficient required", ErrInvalidDecimal)
	}
	if d.Coefficient == "-0" || d.Coefficient == "+0" {
		return fmt.Errorf("%w: negative or signed zero is not canonical", ErrInvalidDecimal)
	}
	if d.Coefficient == "0" && d.Scale != 0 {
		return fmt.Errorf("%w: zero must use scale 0", ErrInvalidDecimal)
	}
	if d.Coefficient[0] == '+' {
		return fmt.Errorf("%w: leading plus is not canonical", ErrInvalidDecimal)
	}
	digits := d.Coefficient
	if digits[0] == '-' {
		digits = digits[1:]
	}
	if digits == "" {
		return fmt.Errorf("%w: coefficient digits required", ErrInvalidDecimal)
	}
	if len(digits) > MaxDecimalCoefficientDigits {
		return fmt.Errorf("%w: coefficient has %d digits, max %d", ErrDecimalOverflow, len(digits), MaxDecimalCoefficientDigits)
	}
	if len(digits) > 1 && digits[0] == '0' {
		return fmt.Errorf("%w: coefficient has leading zero", ErrInvalidDecimal)
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return fmt.Errorf("%w: coefficient is not base ten", ErrInvalidDecimal)
		}
	}
	if d.Coefficient != "0" && d.Scale > 0 && digits[len(digits)-1] == '0' {
		return fmt.Errorf("%w: insignificant trailing fractional zero", ErrInvalidDecimal)
	}
	return nil
}

// Normalize returns the unique canonical representation of d. It accepts
// leading zeroes, signed zero, and trailing fractional zeroes as input but
// never returns them. It does not round.
func (d Decimal) Normalize() (Decimal, error) {
	if d.Coefficient == "" {
		return Decimal{}, fmt.Errorf("%w: coefficient required", ErrInvalidDecimal)
	}
	if len(d.Coefficient) > MaxDecimalLiteralBytes {
		return Decimal{}, fmt.Errorf("%w: coefficient literal exceeds %d bytes", ErrDecimalOverflow, MaxDecimalLiteralBytes)
	}
	if d.Scale > MaxDecimalScale {
		return Decimal{}, fmt.Errorf("%w: scale %d exceeds %d", ErrInvalidDecimal, d.Scale, MaxDecimalScale)
	}
	s := d.Coefficient
	negative := false
	switch s[0] {
	case '-':
		negative = true
		s = s[1:]
	case '+':
		return Decimal{}, fmt.Errorf("%w: leading plus is not allowed", ErrInvalidDecimal)
	}
	if s == "" {
		return Decimal{}, fmt.Errorf("%w: coefficient digits required", ErrInvalidDecimal)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return Decimal{}, fmt.Errorf("%w: coefficient is not base ten", ErrInvalidDecimal)
		}
	}
	s = strings.TrimLeft(s, "0")
	if s == "" {
		return Decimal{Coefficient: "0"}, nil
	}
	// Only zeroes represented by fractional scale are insignificant. Removing
	// more zeroes than the scale would change 1000/2 (10) into 1/0 (1), so
	// decrement the scale one place at a time and stop at scale zero.
	for d.Scale > 0 && strings.HasSuffix(s, "0") {
		s = strings.TrimSuffix(s, "0")
		d.Scale--
	}
	if len(s) > MaxDecimalCoefficientDigits {
		return Decimal{}, fmt.Errorf("%w: coefficient has %d digits, max %d", ErrDecimalOverflow, len(s), MaxDecimalCoefficientDigits)
	}
	if negative {
		s = "-" + s
	}
	out := Decimal{Coefficient: s, Scale: d.Scale}
	if err := out.Validate(); err != nil {
		return Decimal{}, err
	}
	return out, nil
}

// Canonical is a descriptive alias for Normalize used by serializers that
// treat the normalized value as the canonical decimal contract.
func (d Decimal) Canonical() (Decimal, error) { return d.Normalize() }

// ParseDecimal parses a bounded decimal or scientific-notation literal
// without using floating point. Whitespace, NaN, infinity and unbounded
// exponent expansion are rejected. The result is canonical.
func ParseDecimal(raw string) (Decimal, error) {
	if raw == "" || len(raw) > MaxDecimalLiteralBytes {
		return Decimal{}, fmt.Errorf("%w: invalid literal", ErrInvalidDecimal)
	}
	if strings.TrimSpace(raw) != raw {
		return Decimal{}, fmt.Errorf("%w: surrounding whitespace", ErrInvalidDecimal)
	}
	main := raw
	exponent := 0
	if i := strings.IndexAny(raw, "eE"); i >= 0 {
		if strings.ContainsAny(raw[i+1:], "eE") {
			return Decimal{}, fmt.Errorf("%w: multiple exponents", ErrInvalidDecimal)
		}
		main = raw[:i]
		expRaw := raw[i+1:]
		if expRaw == "" {
			return Decimal{}, fmt.Errorf("%w: exponent required", ErrInvalidDecimal)
		}
		var err error
		exponent, err = parseBoundedExponent(expRaw)
		if err != nil {
			return Decimal{}, err
		}
	}
	negative := false
	if strings.HasPrefix(main, "-") {
		negative = true
		main = main[1:]
	} else if strings.HasPrefix(main, "+") {
		return Decimal{}, fmt.Errorf("%w: leading plus is not allowed", ErrInvalidDecimal)
	}
	if main == "" {
		return Decimal{}, fmt.Errorf("%w: significand required", ErrInvalidDecimal)
	}
	dot := strings.IndexByte(main, '.')
	if dot >= 0 && strings.IndexByte(main[dot+1:], '.') >= 0 {
		return Decimal{}, fmt.Errorf("%w: multiple decimal points", ErrInvalidDecimal)
	}
	whole, fraction := main, ""
	if dot >= 0 {
		whole, fraction = main[:dot], main[dot+1:]
	}
	if whole == "" && fraction == "" {
		return Decimal{}, fmt.Errorf("%w: significand digits required", ErrInvalidDecimal)
	}
	for i := 0; i < len(whole); i++ {
		if whole[i] < '0' || whole[i] > '9' {
			return Decimal{}, fmt.Errorf("%w: invalid significand", ErrInvalidDecimal)
		}
	}
	for i := 0; i < len(fraction); i++ {
		if fraction[i] < '0' || fraction[i] > '9' {
			return Decimal{}, fmt.Errorf("%w: invalid significand", ErrInvalidDecimal)
		}
	}
	digits := whole + fraction
	if digits == "" {
		return Decimal{}, fmt.Errorf("%w: significand digits required", ErrInvalidDecimal)
	}
	scale := len(fraction) - exponent
	if scale < 0 {
		zeros := -scale
		if len(digits)+zeros > MaxDecimalCoefficientDigits+MaxDecimalLiteralBytes {
			return Decimal{}, fmt.Errorf("%w: exponent expansion too large", ErrDecimalOverflow)
		}
		digits += strings.Repeat("0", zeros)
		scale = 0
	}
	if scale > 255 {
		return Decimal{}, fmt.Errorf("%w: exponent scale too large", ErrDecimalOverflow)
	}
	out := Decimal{Coefficient: digits, Scale: uint8(scale)}
	if negative {
		out.Coefficient = "-" + out.Coefficient
	}
	normalized, err := out.Normalize()
	if err != nil {
		return Decimal{}, err
	}
	return normalized, nil
}

func parseBoundedExponent(raw string) (int, error) {
	if raw == "" {
		return 0, fmt.Errorf("%w: exponent required", ErrInvalidDecimal)
	}
	negative := false
	if raw[0] == '+' || raw[0] == '-' {
		negative = raw[0] == '-'
		raw = raw[1:]
	}
	if raw == "" || len(raw) > 4 {
		return 0, fmt.Errorf("%w: exponent out of bounds", ErrDecimalOverflow)
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			return 0, fmt.Errorf("%w: invalid exponent", ErrInvalidDecimal)
		}
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n > MaxDecimalLiteralBytes {
		return 0, fmt.Errorf("%w: exponent out of bounds", ErrDecimalOverflow)
	}
	if negative {
		n = -n
	}
	return n, nil
}

// CanonicalString returns the unambiguous coefficient/scale form used in
// diagnostics and tests (for example, 123/2 represents 1.23).
func (d Decimal) CanonicalString() string {
	n, err := d.Normalize()
	if err != nil {
		return ""
	}
	return n.Coefficient + "/" + strconv.Itoa(int(n.Scale))
}

// String implements fmt.Stringer using CanonicalString. Invalid values return
// an empty string rather than exposing an untrusted raw lexeme.
func (d Decimal) String() string { return d.CanonicalString() }

// CanonicalJSON returns deterministic JSON for the normalized Decimal.
func (d Decimal) CanonicalJSON() ([]byte, error) {
	n, err := d.Normalize()
	if err != nil {
		return nil, err
	}
	return json.Marshal(decimalWire(n))
}

// MarshalJSON serializes only the normalized exact representation.
func (d Decimal) MarshalJSON() ([]byte, error) { return d.CanonicalJSON() }

// UnmarshalJSON accepts the object representation and normalizes it. Unknown
// object fields are ignored by encoding/json for additive compatibility.
func (d *Decimal) UnmarshalJSON(data []byte) error {
	if d == nil {
		return fmt.Errorf("%w: nil destination", ErrInvalidDecimal)
	}
	var wire decimalWire
	if err := decodeV2JSON(data, "decimal", &wire); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidDecimal, err)
	}
	n, err := Decimal(wire).Normalize()
	if err != nil {
		return err
	}
	*d = n
	return nil
}

// decimalWire prevents Decimal.MarshalJSON from recursively invoking itself
// while keeping the public JSON field names explicit.
type decimalWire struct {
	Coefficient string `json:"coefficient"`
	Scale       uint8  `json:"scale"`
}

// ToRat returns an exact rational copy of d.
func (d Decimal) ToRat() (*big.Rat, error) {
	n, err := d.Normalize()
	if err != nil {
		return nil, err
	}
	coefficient, ok := new(big.Int).SetString(n.Coefficient, 10)
	if !ok {
		return nil, fmt.Errorf("%w: coefficient", ErrInvalidDecimal)
	}
	denominator := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n.Scale)), nil)
	return new(big.Rat).SetFrac(coefficient, denominator), nil
}

// Rat is a concise alias for ToRat.
func (d Decimal) Rat() (*big.Rat, error) { return d.ToRat() }

// ToNanoUnits converts d to checked integer ledger nanos without rounding.
// Values with finer-than-nano precision are rejected unless the discarded
// digits are all zero.
func (d Decimal) ToNanoUnits() (int64, error) {
	n, err := d.Normalize()
	if err != nil {
		return 0, err
	}
	coefficient, ok := new(big.Int).SetString(n.Coefficient, 10)
	if !ok {
		return 0, fmt.Errorf("%w: coefficient", ErrInvalidDecimal)
	}
	if n.Scale <= LedgerNanoScale {
		coefficient.Mul(coefficient, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(LedgerNanoScale-n.Scale)), nil))
	} else {
		divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n.Scale-LedgerNanoScale)), nil)
		quotient, remainder := new(big.Int), new(big.Int)
		quotient.QuoRem(coefficient, divisor, remainder)
		if remainder.Sign() != 0 {
			return 0, fmt.Errorf("%w: value has sub-nano precision", ErrDecimalPrecision)
		}
		coefficient = quotient
	}
	if !coefficient.IsInt64() {
		return 0, fmt.Errorf("%w: ledger nanos", ErrDecimalOverflow)
	}
	return coefficient.Int64(), nil
}

// ToLedgerNanos is an explicit spelling of ToNanoUnits for ledger adapters.
func (d Decimal) ToLedgerNanos() (int64, error) { return d.ToNanoUnits() }

// DecimalFromNanoUnits constructs an exact Decimal from checked ledger nanos.
func DecimalFromNanoUnits(nanos int64) Decimal {
	n, err := (Decimal{Coefficient: strconv.FormatInt(nanos, 10), Scale: LedgerNanoScale}).Normalize()
	if err != nil {
		// strconv.FormatInt and a fixed scale are always representable. Keep this
		// panic unreachable rather than returning an invalid Decimal silently.
		panic(err)
	}
	return n
}

// Equal compares normalized exact values without converting through float64.
func (d Decimal) Equal(other Decimal) bool {
	a, errA := d.Normalize()
	b, errB := other.Normalize()
	return errA == nil && errB == nil && a == b
}
