package app

import "context"

const (
	TransitionClaimFailed      = "claim_failed"
	TransitionValidationFailed = "validation_failed"
	TransitionAcquireFailed    = "acquire_failed"
)

type ProcessMetrics interface {
	ObserveTransition(state, kind, providerID string)
	RefreshAfterBatch(ctx context.Context)
}
