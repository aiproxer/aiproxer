package economics

import (
	"fmt"
	"math"
	"math/big"
	"math/bits"
)

const tokensPerMillion = 1_000_000

// Money is a currency-tagged amount in nano-units of the major currency unit.
// Present distinguishes authoritative zero from an absent amount (requirement 6.2).
type Money struct {
	NanoUnits int64  `json:"nano_units"`
	Currency  string `json:"currency,omitempty"`
	Present   bool   `json:"present"`
}

// Validate accepts absent money; present money must be nonnegative with a
// normalized currency code (requirements 4.6, 4.8).
func (m Money) Validate() error {
	return ValidatePresentMoney(m)
}

// AggregateMoney sums present same-currency amounts with checked arithmetic.
func AggregateMoney(parts ...Money) (Money, error) {
	if len(parts) == 0 {
		return Money{}, fmt.Errorf("economics: AggregateMoney requires at least one amount")
	}
	sum := parts[0]
	if err := sum.Validate(); err != nil {
		return Money{}, err
	}
	if !sum.Present {
		return Money{}, fmt.Errorf("economics: AggregateMoney operands must be present")
	}
	for i := 1; i < len(parts); i++ {
		var err error
		sum, err = sum.Add(parts[i])
		if err != nil {
			return Money{}, err
		}
	}
	return sum, nil
}

// Add returns the checked sum of two present amounts in the same currency.
func (m Money) Add(other Money) (Money, error) {
	cur, err := requirePresentSameCurrency(m, other)
	if err != nil {
		return Money{}, err
	}
	sum, ok := addMoneyChecked(m.NanoUnits, other.NanoUnits)
	if !ok {
		return Money{}, fmt.Errorf("economics: money add overflow")
	}
	return Money{NanoUnits: sum, Currency: cur, Present: true}, nil
}

// Sub returns the checked difference m - other for non-negative present amounts.
func (m Money) Sub(other Money) (Money, error) {
	cur, err := requirePresentSameCurrency(m, other)
	if err != nil {
		return Money{}, err
	}
	diff, ok := subMoneyChecked(m.NanoUnits, other.NanoUnits)
	if !ok {
		return Money{}, fmt.Errorf("economics: money sub underflow")
	}
	return Money{NanoUnits: diff, Currency: cur, Present: true}, nil
}

// MulTokensByRatePer1M multiplies token count by a per-1M nano rate with overflow
// detection. A successful result is always Present, including authoritative zero
// when tokens or rate are non-positive (mirrors Phase 2 checked multiply semantics).
// Fractional nanos truncate toward zero (RoundingUnspecified default).
func MulTokensByRatePer1M(tokens, pricePer1MNano int64) (Money, error) {
	if tokens < 0 || pricePer1MNano < 0 {
		return Money{}, fmt.Errorf("economics: tokens and rate must be non-negative")
	}
	if tokens == 0 || pricePer1MNano == 0 {
		return Money{NanoUnits: 0, Present: true}, nil
	}
	hi, lo := bits.Mul64(uint64(tokens), uint64(pricePer1MNano))
	div := uint64(tokensPerMillion)
	if hi >= div {
		return Money{}, fmt.Errorf("economics: token*rate overflow")
	}
	q, _ := bits.Div64(hi, lo, div)
	if q > math.MaxInt64 {
		return Money{}, fmt.Errorf("economics: token*rate overflow")
	}
	return Money{NanoUnits: int64(q), Present: true}, nil
}

// TokensFromMoneyPer1M converts a money amount in nano-units into a token count
// given a per-1M nano rate, using checked rational rounding (output-limit
// inversion helper; requirements 4.6, 4.10). Unrepresentable int64 results are
// rejected; values never saturate at MaxInt64.
func TokensFromMoneyPer1M(moneyNano, pricePer1MNano int64, policy RoundingPolicy) (int64, error) {
	if moneyNano < 0 || pricePer1MNano < 0 {
		return 0, fmt.Errorf("economics: money and rate must be non-negative")
	}
	if pricePer1MNano == 0 {
		return 0, fmt.Errorf("economics: zero rate")
	}
	if moneyNano == 0 {
		return 0, nil
	}
	if policy == RoundingUnspecified {
		policy = RoundingTowardZero
	}
	if !policy.IsKnown() {
		return 0, fmt.Errorf("economics: unknown rounding policy %q", policy)
	}
	hi, lo := bits.Mul64(uint64(moneyNano), uint64(tokensPerMillion))
	div := uint64(pricePer1MNano)
	if hi >= div {
		return 0, fmt.Errorf("economics: token inversion overflow")
	}
	q, rem := bits.Div64(hi, lo, div)
	if rem == 0 || policy == RoundingTowardZero || policy == RoundingFloor {
		if q > math.MaxInt64 {
			return 0, fmt.Errorf("economics: token inversion overflow")
		}
		return int64(q), nil
	}
	numer := new(big.Int).Mul(big.NewInt(moneyNano), big.NewInt(tokensPerMillion))
	r := new(big.Rat).SetFrac(numer, big.NewInt(pricePer1MNano))
	return RoundToInt64(r, policy)
}

func requirePresentSameCurrency(a, b Money) (string, error) {
	if !a.Present || !b.Present {
		return "", fmt.Errorf("economics: both money operands must be present")
	}
	ac, err := NormalizeCurrency(a.Currency)
	if err != nil {
		return "", err
	}
	bc, err := NormalizeCurrency(b.Currency)
	if err != nil {
		return "", err
	}
	if ac != bc {
		return "", fmt.Errorf("economics: currency mismatch %q vs %q", a.Currency, b.Currency)
	}
	return ac, nil
}

func addMoneyChecked(a, b int64) (int64, bool) {
	if a < 0 || b < 0 {
		return 0, false
	}
	sum, carry := bits.Add64(uint64(a), uint64(b), 0)
	if carry != 0 || sum > math.MaxInt64 {
		return 0, false
	}
	return int64(sum), true
}

func subMoneyChecked(a, b int64) (int64, bool) {
	if a < 0 || b < 0 || a < b {
		return 0, false
	}
	return a - b, true
}
