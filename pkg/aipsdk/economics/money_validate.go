package economics

import (
	"fmt"
	"strings"
	"unicode"
)

// ValidatePresentMoney checks present money for nonnegative nano-units and
// normalized currency (requirements 4.6, 4.8, 4.9). Absent money is valid and
// distinct from authoritative zero.
func ValidatePresentMoney(m Money) error {
	if !m.Present {
		return nil
	}
	if m.NanoUnits < 0 {
		return fmt.Errorf("economics: negative money")
	}
	if _, err := NormalizeCurrency(m.Currency); err != nil {
		return err
	}
	return nil
}

// NormalizeCurrency trims and requires a nonempty uppercase A–Z currency code.
// Lowercase or mixed-case codes are rejected (requirement 4.6).
func NormalizeCurrency(currency string) (string, error) {
	c := strings.TrimSpace(currency)
	if c == "" {
		return "", fmt.Errorf("economics: empty present currency")
	}
	if c != strings.ToUpper(c) {
		return "", fmt.Errorf("economics: unnormalized currency %q", currency)
	}
	for _, r := range c {
		if r < 'A' || r > 'Z' {
			return "", fmt.Errorf("economics: invalid currency %q", currency)
		}
	}
	return c, nil
}

// ValidateSafeRef rejects empty or control-bearing opaque references used on
// durable/operator surfaces (design D14).
func ValidateSafeRef(field, value string) error {
	v := strings.TrimSpace(value)
	if v == "" {
		return fmt.Errorf("economics: %s required", field)
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f || !unicode.IsPrint(r) {
			return fmt.Errorf("economics: %s contains unsafe characters", field)
		}
	}
	return nil
}

// IsKnown reports whether p is a documented rounding policy.
func (p RoundingPolicy) IsKnown() bool {
	switch p {
	case RoundingUnspecified, RoundingHalfAwayFromZero, RoundingHalfEven, RoundingTowardZero, RoundingFloor:
		return true
	}
	return false
}
