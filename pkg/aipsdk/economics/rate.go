package economics

import (
	"fmt"
	"math/big"
	"strings"
)

// NanoRate is a monetary rate in nano-units of the major currency unit per
// pricing basis (for example per 1M tokens). Present distinguishes an
// authoritative zero rate from an absent/unspecified rate (requirements 2.9, 4.8).
type NanoRate struct {
	NanoUnits int64
	Present   bool
}

const nanosPerMajorUnit = int64(1_000_000_000)

// ParseDecimalToNano converts a non-negative decimal major-unit amount into
// nano-units using exact integer arithmetic (requirements 4.6, 4.10).
//
// Strict grammar (ASCII only):
//   - one or more digits, optionally followed by '.' and one to nine digits
//   - digits are required before the decimal point; trailing '.' is rejected
//   - leading zeros are allowed ("01.5", "0.5")
//   - rejected: '+'/'-', exponent, fraction slash, hex, underscores, any
//     whitespace (including outer), more than nine fractional digits, overflow
//
// Floats and silent Int64 truncation are never used.
func ParseDecimalToNano(raw string) (int64, error) {
	if raw == "" {
		return 0, fmt.Errorf("economics: empty decimal")
	}
	dot := -1
	for i := range len(raw) {
		c := raw[i]
		if c == '.' {
			if dot >= 0 {
				return 0, fmt.Errorf("economics: invalid decimal %q", raw)
			}
			dot = i
			continue
		}
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("economics: invalid decimal %q", raw)
		}
	}
	var intPart, fracPart string
	if dot < 0 {
		intPart = raw
	} else {
		intPart = raw[:dot]
		fracPart = raw[dot+1:]
		if intPart == "" {
			return 0, fmt.Errorf("economics: invalid decimal %q", raw)
		}
		if fracPart == "" {
			return 0, fmt.Errorf("economics: invalid decimal %q", raw)
		}
		if len(fracPart) > 9 {
			return 0, fmt.Errorf("economics: has more than 9 decimal places")
		}
	}
	for len(fracPart) < 9 {
		fracPart += "0"
	}
	whole := new(big.Int)
	if _, ok := whole.SetString(intPart, 10); !ok {
		return 0, fmt.Errorf("economics: invalid decimal %q", raw)
	}
	frac := new(big.Int)
	if _, ok := frac.SetString(fracPart, 10); !ok {
		return 0, fmt.Errorf("economics: invalid decimal %q", raw)
	}
	nanos := new(big.Int).Mul(whole, big.NewInt(nanosPerMajorUnit))
	nanos.Add(nanos, frac)
	if !nanos.IsInt64() {
		return 0, fmt.Errorf("economics: nano overflow")
	}
	return nanos.Int64(), nil
}

// ParseOptionalNanoRate treats empty/whitespace-only as absent; otherwise parses
// an explicit rate including authoritative zero. Non-empty values must satisfy
// ParseDecimalToNano (no surrounding whitespace on a present rate).
func ParseOptionalNanoRate(raw string) (NanoRate, error) {
	if strings.TrimSpace(raw) == "" {
		return NanoRate{}, nil
	}
	n, err := ParseDecimalToNano(raw)
	if err != nil {
		return NanoRate{}, err
	}
	return NanoRate{NanoUnits: n, Present: true}, nil
}

// ParseRequiredNanoRate rejects absent rates; explicit zero remains valid.
func ParseRequiredNanoRate(raw string) (NanoRate, error) {
	if strings.TrimSpace(raw) == "" {
		return NanoRate{}, fmt.Errorf("economics: required rate absent")
	}
	n, err := ParseDecimalToNano(raw)
	if err != nil {
		return NanoRate{}, err
	}
	return NanoRate{NanoUnits: n, Present: true}, nil
}
