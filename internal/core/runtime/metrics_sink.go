package runtime

import "github.com/aiproxer/aiproxer/pkg/aipapi"

// MetricsSink receives coarse executor-level observations (Prometheus, etc.) when non-nil.
type MetricsSink interface {
	OnAttemptRecorded(outcome aipapi.AttemptOutcome, backend string)
	OnBackendOpenDuration(backend string, seconds float64)
	OnTransportNegotiation(operation aipapi.Operation, mode aipapi.TransportMode, outcome string)
	OnCancellation(obs CancellationObservation)
}

func (e *Executor) recordTransportNegotiation(operation aipapi.Operation, mode aipapi.TransportMode, outcome string) {
	if e == nil || e.Metrics == nil {
		return
	}
	e.Metrics.OnTransportNegotiation(operation, mode, outcome)
}

// ReasoningReplaySkipObserver is an optional MetricsSink extension for the
// bounded reasoning_replay_skipped{backend} counter. Sinks opt in by
// implementing it; the executor type-asserts so existing sinks keep working
// unchanged.
type ReasoningReplaySkipObserver interface {
	OnReasoningReplaySkipped(backend string)
}

func (e *Executor) recordReasoningReplaySkipped(backend string) {
	if e == nil || e.Metrics == nil {
		return
	}
	if o, ok := e.Metrics.(ReasoningReplaySkipObserver); ok && o != nil {
		o.OnReasoningReplaySkipped(backend)
	}
}

// EmptyVisibleTurnObserver is an optional MetricsSink extension for the bounded
// empty_visible_turn_total{backend} counter: one increment per clean terminal
// turn whose surviving attempt produced no visible output. Sinks opt in by
// implementing it; the executor type-asserts so existing sinks keep working
// unchanged.
type EmptyVisibleTurnObserver interface {
	OnEmptyVisibleTurn(backend string)
}

func (e *Executor) recordEmptyVisibleTurn(backend string) {
	if e == nil || e.Metrics == nil {
		return
	}
	if o, ok := e.Metrics.(EmptyVisibleTurnObserver); ok && o != nil {
		o.OnEmptyVisibleTurn(backend)
	}
}

// EmptyVisibleTurnRetryObserver is an optional MetricsSink extension for the
// bounded empty_visible_turn_retry_total{backend, outcome} counter: one
// increment per corrective-retry decision for a candidate empty visible turn,
// with the closed outcome set corrected|skipped|failed. Sinks opt in by
// implementing it; the executor type-asserts so existing sinks keep working
// unchanged.
type EmptyVisibleTurnRetryObserver interface {
	OnEmptyVisibleTurnRetry(backend, outcome string)
}

func (e *Executor) recordEmptyVisibleTurnRetry(backend, outcome string) {
	if e == nil || e.Metrics == nil {
		return
	}
	if o, ok := e.Metrics.(EmptyVisibleTurnRetryObserver); ok && o != nil {
		o.OnEmptyVisibleTurnRetry(backend, outcome)
	}
}

func (e *Executor) recordCancellation(obs CancellationObservation) {
	if e == nil || e.Metrics == nil {
		return
	}
	e.Metrics.OnCancellation(obs)
}

// The background funding refusal reasons. The set is closed and content-free:
// a reason never names a principal, an account, a sponsor or a request.
const (
	// ReasonSponsorshipUndeclared counts a background operation refused
	// because the operator declared no funding mode.
	ReasonSponsorshipUndeclared = "mode_undeclared"
	// ReasonSponsorshipSponsorUndeclared counts a background operation
	// refused because its sponsor key is not one the operator declared.
	ReasonSponsorshipSponsorUndeclared = "sponsor_key_undeclared"
	// ReasonSponsorshipMonetaryHostIncomplete counts a customer-funded
	// background operation refused because the host carries no complete
	// monetary binding.
	ReasonSponsorshipMonetaryHostIncomplete = "monetary_host_incomplete"
	// ReasonSponsorshipHandoffNotReady counts a customer-funded background
	// operation refused because the durable terminal handoff cannot accept a
	// new obligation right now.
	ReasonSponsorshipHandoffNotReady = "handoff_not_ready"
)

// BackgroundSponsorshipObserver is an optional MetricsSink extension for the
// bounded background_sponsorship_refused{reason} counter: one increment per
// background operation refused before any provider dispatch. Sinks opt in by
// implementing it; the executor type-asserts so existing sinks keep working
// unchanged.
type BackgroundSponsorshipObserver interface {
	OnBackgroundSponsorshipRefused(reason string)
}

func (e *Executor) recordBackgroundSponsorshipRefused(reason string) {
	if e == nil || e.Metrics == nil {
		return
	}
	if o, ok := e.Metrics.(BackgroundSponsorshipObserver); ok && o != nil {
		o.OnBackgroundSponsorshipRefused(reason)
	}
}
