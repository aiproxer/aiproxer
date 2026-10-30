package reasoningpreservation

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
)

// CompanionPolicy is an internal composition seam for a provider companion.
// The generic feature owns state matching/restoration; composition owns any
// provider-specific trust protocol.
type CompanionPolicy struct {
	BeforeMatch  func(*aipapi.Call, request.AttemptMeta)
	AfterRestore func(context.Context, *aipapi.Call, request.AttemptMeta, MatchResult, RestoreResult)
}
