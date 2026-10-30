package reasoninge2e

import (
	"fmt"
	"math/rand/v2"
)

// ResponsesPresenceVariant is a deterministic exact-item presence case for seeded smokes.
type ResponsesPresenceVariant string

const (
	ResponsesPresenceEncryptedAbsent ResponsesPresenceVariant = "enc_absent"
	ResponsesPresenceEncryptedNull   ResponsesPresenceVariant = "enc_null"
	ResponsesPresenceEncryptedValue  ResponsesPresenceVariant = "enc_value"
	ResponsesPresenceWithContent     ResponsesPresenceVariant = "with_content"
)

// ResponsesMatrixCase is one reproducible Responses-inclusive smoke cell.
type ResponsesMatrixCase struct {
	Seed     uint64
	Variant  ResponsesPresenceVariant
	StreamFE bool
	Trace    string
}

// DefaultResponsesSmokeCases returns a moderate fixed-seed set (8 cells) for local smoke.
func DefaultResponsesSmokeCases() []ResponsesMatrixCase {
	return ResponsesSmokeCases(0x52E5017E, 8)
}

// ResponsesSmokeCases builds n deterministic Responses presence/stream cells from seedBase.
func ResponsesSmokeCases(seedBase uint64, n int) []ResponsesMatrixCase {
	if n <= 0 {
		n = 8
	}
	variants := []ResponsesPresenceVariant{
		ResponsesPresenceEncryptedAbsent,
		ResponsesPresenceEncryptedNull,
		ResponsesPresenceEncryptedValue,
		ResponsesPresenceWithContent,
	}
	out := make([]ResponsesMatrixCase, 0, n)
	for i := range n {
		seed := seedBase + uint64(i)*0x9E3779B97F4A7C15
		rng := rand.New(rand.NewPCG(seed, 0)) //nolint:gosec // deterministic test plan, not crypto
		v := variants[rng.IntN(len(variants))]
		stream := rng.IntN(2) == 0
		out = append(out, ResponsesMatrixCase{
			Seed:     seed,
			Variant:  v,
			StreamFE: stream,
			Trace:    fmt.Sprintf("responses_smoke seed=%d variant=%s stream_fe=%v", seed, v, stream),
		})
	}
	return out
}
