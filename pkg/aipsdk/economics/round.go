package economics

import (
	"fmt"
	"math/big"
)

// RoundQuotient rounds numer/denom to int64 using a declared RoundingPolicy.
// Zero denominators are rejected before constructing a rational.
func RoundQuotient(numer, denom int64, policy RoundingPolicy) (int64, error) {
	if denom == 0 {
		return 0, fmt.Errorf("economics: zero denominator")
	}
	return RoundToInt64(big.NewRat(numer, denom), policy)
}

// RoundToInt64 rounds a rational value to int64 using a declared RoundingPolicy.
// RoundingUnspecified follows existing toward-zero catalog multiply behavior.
// Ceil is not implemented. Nil rats, unknown policies, and overflow are rejected
// (requirements 4.6, 4.10).
func RoundToInt64(r *big.Rat, policy RoundingPolicy) (int64, error) {
	if r == nil {
		return 0, fmt.Errorf("economics: nil rational")
	}
	if r.Denom().Sign() == 0 {
		return 0, fmt.Errorf("economics: zero denominator")
	}
	if !policy.IsKnown() {
		return 0, fmt.Errorf("economics: unknown rounding policy %q", policy)
	}
	if policy == RoundingUnspecified {
		policy = RoundingTowardZero
	}

	num := new(big.Int).Set(r.Num())
	den := new(big.Int).Set(r.Denom())
	quo := new(big.Int)
	rem := new(big.Int)
	quo.QuoRem(num, den, rem)
	if rem.Sign() == 0 {
		return int64Checked(quo)
	}

	switch policy {
	case RoundingTowardZero:
		// QuoRem already truncates toward zero.
	case RoundingFloor:
		if num.Sign() < 0 {
			quo.Sub(quo, big.NewInt(1))
		}
	case RoundingHalfAwayFromZero, RoundingHalfEven:
		absRem := new(big.Int).Abs(rem)
		twice := new(big.Int).Lsh(absRem, 1)
		cmp := twice.Cmp(den)
		roundAway := false
		switch {
		case cmp > 0:
			roundAway = true
		case cmp == 0:
			if policy == RoundingHalfAwayFromZero {
				roundAway = true
			} else if quo.Bit(0) == 1 {
				// Half-even: round away from zero when the truncated quotient is odd.
				roundAway = true
			}
		}
		if roundAway {
			if num.Sign() < 0 {
				quo.Sub(quo, big.NewInt(1))
			} else {
				quo.Add(quo, big.NewInt(1))
			}
		}
	default:
		return 0, fmt.Errorf("economics: unknown rounding policy %q", policy)
	}
	return int64Checked(quo)
}

func int64Checked(v *big.Int) (int64, error) {
	if v == nil || !v.IsInt64() {
		return 0, fmt.Errorf("economics: rounded value overflow")
	}
	return v.Int64(), nil
}
