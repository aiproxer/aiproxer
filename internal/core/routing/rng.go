package routing

import (
	randv2 "math/rand/v2"
)

// v2Rng adapts *rand/v2.Rand to [Rng] (Intn delegates to IntN).
type v2Rng struct{ r *randv2.Rand }

func (v v2Rng) Intn(n int) int { return v.r.IntN(n) }

// NewSeededRng returns a deterministic, non-crypto-strength RNG for weighted routing.
// The same seed yields the same stream (PCG with a zero second word).
func NewSeededRng(seed int64) Rng {
	// security: uint64(seed) is intentional two's-complement reinterpretation for PCG state, not a numeric range cast.
	// security: math/rand/v2 is for reproducible weighted routing only, never for secrets or tokens.
	return v2Rng{r: randv2.New(randv2.NewPCG(uint64(seed), 0))} // #nosec G115,G404
}

// WrapRandV2 wraps a *rand/v2.Rand as [Rng] (for example a single shared instance in tests).
func WrapRandV2(r *randv2.Rand) Rng {
	if r == nil {
		return nil
	}
	return v2Rng{r: r}
}
