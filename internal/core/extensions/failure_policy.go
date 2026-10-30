package extensions

import (
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

// FailurePolicy describes default runner behavior when a stage handler errors (design section 17).
type FailurePolicy uint8

const (
	// FailurePolicyUnset means the stage id is not in the legal pipeline.
	FailurePolicyUnset FailurePolicy = iota
	FailurePolicyFailOpen
	FailurePolicyFailClosed
)

// Stage name constants for the legal pipeline (stable ids; align with pkg/aipsdk/feature).
// Only aliases with non-test production users are kept; tests must use
// feature.StageID* directly for the remaining stages.
const (
	StageSessionOpen               = feature.StageIDSessionOpen
	StageToolCatalog               = feature.StageIDToolCatalog
	StageCandidateAttemptTransform = feature.StageIDCandidateAttemptTransform
	StageToolEventReaction         = feature.StageIDToolEventReaction
	StageFinalStreamObservation    = feature.StageIDFinalStreamObservation
)

// DefaultFailurePolicyForStage returns the documented default for the stage (design section 17).
func DefaultFailurePolicyForStage(stage string) FailurePolicy {
	switch stage {
	case feature.StageIDSessionOpen, feature.StageIDSessionClassification:
		return FailurePolicyFailOpen
	case feature.StageIDTransportAuth, feature.StageIDSecretGuard:
		return FailurePolicyFailClosed
	case feature.StageIDSubmit, feature.StageIDToolCatalog, feature.StageIDRequestWide, feature.StageIDPreRequest:
		// Pre-backend mutation: compatible with hook-bus per-hook FailureMode; stage default fail-open.
		return FailurePolicyFailOpen
	case feature.StageIDRouteHinting:
		return FailurePolicyFailOpen
	case feature.StageIDCandidateAttemptTransform:
		return FailurePolicyFailOpen
	case feature.StageIDAttemptLifecycle:
		return FailurePolicyFailOpen
	case feature.StageIDStreamEventMutation, feature.StageIDToolEventReaction:
		// Per-hook policies apply; documented stage default matches lenient hook-bus handling.
		return FailurePolicyFailOpen
	case feature.StageIDCompletionGating:
		return FailurePolicyFailOpen
	case feature.StageIDFinalStreamObservation:
		return FailurePolicyFailOpen
	case feature.StageIDTrafficObservation:
		return FailurePolicyFailOpen
	case feature.StageIDEgressEncoding:
		return FailurePolicyFailClosed
	default:
		return FailurePolicyUnset
	}
}

// FailurePolicyLabel returns a stable JSON label for inventory/diagnostics.
func FailurePolicyLabel(p FailurePolicy) string {
	switch p {
	case FailurePolicyFailOpen:
		return "fail_open"
	case FailurePolicyFailClosed:
		return "fail_closed"
	default:
		return "unset"
	}
}
