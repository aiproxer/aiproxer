package reasoningpreservation

import (
	"context"
	"errors"
)

// ReservationOutcome is a typed content-free outcome of the reservation stage.
type ReservationOutcome string

const (
	ReservationReserved              ReservationOutcome = "reserved"
	ReservationSkippedIneligible     ReservationOutcome = "skipped_ineligible"
	ReservationSkippedBelowThreshold ReservationOutcome = "skipped_below_threshold"
	ReservationNotFound              ReservationOutcome = "not_found"
	ReservationConflict              ReservationOutcome = "conflict"
	ReservationBudgetExceeded        ReservationOutcome = "budget_exceeded"
	ReservationError                 ReservationOutcome = "error"
)

// ReservationResult is passed to next hooks in the post-append chain.
// It is content-free: no reasoning text, no credentials, no raw hashes.
type ReservationResult struct {
	Outcome     ReservationOutcome
	Claim       CompressionClaim
	Correlation PostAppendCorrelation
	Err         error
}

// IsReserved reports whether reservation succeeded.
func (r ReservationResult) IsReserved() bool { return r.Outcome == ReservationReserved }

// TryReserveCompression performs the local reservation BEFORE any egress/provider call.
// It checks ineligibility and MinSourceBytes, then calls ReserveCompression with the
// correct SemanticDigest from correlation and provisional EgressPolicyRefHash.
// On budget/conflict/notfound it returns typed outcome and leaves original untouched.
// It never evicts original reasoning and never calls provider.
func TryReserveCompression(ctx context.Context, cfg Config, store CompressionStore, corr PostAppendCorrelation) ReservationResult {
	if store == nil {
		return ReservationResult{Outcome: ReservationNotFound, Correlation: corr, Err: ErrCompressionNotFound}
	}
	if !cfg.Compression.Enabled {
		return ReservationResult{Outcome: ReservationSkippedIneligible, Correlation: corr}
	}
	var zeroDigest [32]byte
	if corr.SemanticDigest == zeroDigest {
		return ReservationResult{Outcome: ReservationSkippedIneligible, Correlation: corr}
	}
	if corr.ArtifactID == "" || corr.PolicyRevision == "" {
		return ReservationResult{Outcome: ReservationConflict, Correlation: corr, Err: ErrCompressionConflict}
	}
	// Source eligibility: below MinSourceBytes => no reserve. Zero source bytes with MinSourceBytes>0 is also below threshold.
	if cfg.Compression.MinSourceBytes > 0 && corr.SourceBytes < cfg.Compression.MinSourceBytes {
		return ReservationResult{Outcome: ReservationSkippedBelowThreshold, Correlation: corr}
	}

	claim, err := store.ReserveCompression(ctx, corr.Partition, corr.ArtifactID, corr.OriginalDigest, corr.PolicyRevision, corr.SemanticDigest, corr.EgressPolicyRefHash)
	if err == nil {
		return ReservationResult{Outcome: ReservationReserved, Claim: claim, Correlation: corr}
	}
	if errors.Is(err, ErrCompressionNotFound) {
		return ReservationResult{Outcome: ReservationNotFound, Correlation: corr, Err: err}
	}
	if errors.Is(err, ErrCompressionBudgetExceeded) {
		return ReservationResult{Outcome: ReservationBudgetExceeded, Correlation: corr, Err: err}
	}
	if errors.Is(err, ErrCompressionConflict) {
		return ReservationResult{Outcome: ReservationConflict, Correlation: corr, Err: err}
	}
	return ReservationResult{Outcome: ReservationError, Correlation: corr, Err: err}
}

// PostReservationStage is the next composable stage after reservation (egress, submit).
// It receives the ReservationResult (including Claim) for chaining.
type PostReservationStage func(ctx context.Context, res ReservationResult) error

// NewCompressionReservationHook returns a concrete LOCAL post-append hook that reserves
// optional capacity before any provider submission. It is invoked unlocked after original
// append success. It returns nil always (fail-open) so original is never invalidated.
// If reservation succeeds (OutcomeReserved) and next is non-nil, it invokes next with
// the ReservationResult; non-reserved outcomes never call next; next errors are fail-open.
func NewCompressionReservationHook(cfg Config, store CompressionStore, next PostReservationStage) PostAppendHook {
	return NewCompressionReservationHookWithTelemetry(cfg, store, next, nil)
}

// NewCompressionReservationHookWithTelemetry is the telemetry-aware reservation hook.
// It records content-free outcomes: below_threshold, ineligible, budget_exceeded (per-session/total)
// and reservation reserved, without emitting reasoning text or IDs.
func NewCompressionReservationHookWithTelemetry(cfg Config, store CompressionStore, next PostReservationStage, tel *Telemetry) PostAppendHook {
	return func(ctx context.Context, corr PostAppendCorrelation) error {
		res := TryReserveCompression(ctx, cfg, store, corr)
		if tel != nil && cfg.Compression.Enabled {
			switch res.Outcome {
			case ReservationSkippedBelowThreshold:
				tel.RecordShadowMeasurement(OutcomeBelowThreshold, corr.SourceBytes, 0, 0, 0, 0)
			case ReservationSkippedIneligible:
				tel.RecordShadowMeasurement(OutcomeCompIneligible, corr.SourceBytes, 0, 0, 0, 0)
			case ReservationBudgetExceeded:
				// Distinguish per-session vs total via error kind if available.
				// errors.AsType keeps the kind reachable when the store wraps
				// its budget error with context.
				if be, ok := errors.AsType[*BudgetError](res.Err); ok {
					switch be.Kind {
					case BudgetPendingPerSession:
						tel.RecordShadowMeasurement(OutcomeBudgetPendingPerSession, corr.SourceBytes, 0, 0, 0, 0)
					case BudgetPendingTotal:
						tel.RecordShadowMeasurement(OutcomeBudgetPendingTotal, corr.SourceBytes, 0, 0, 0, 0)
					default:
						tel.RecordShadowMeasurement(OutcomeReservationBudgetExceeded, corr.SourceBytes, 0, 0, 0, 0)
					}
				} else {
					tel.RecordShadowMeasurement(OutcomeReservationBudgetExceeded, corr.SourceBytes, 0, 0, 0, 0)
				}
			case ReservationReserved:
				// Reservation success is not directly a submitted outcome; submit stage will emit submitted.
				// Still record eligible reservation for observability if needed (no content).
			}
		}
		if res.Outcome == ReservationReserved && next != nil {
			_ = next(ctx, res)
		}
		return nil
	}
}

// BuildPostAppendHook constructs the composable post-append hook chain rooted in compression.
// Chain is reserve -> egress -> submit (4.4). Hook is nil when compression disabled
// to preserve disabled-mode byte equivalency.
func BuildPostAppendHook(cfg Config, store TurnStore, svc CompressionServices) PostAppendHook {
	return BuildPostAppendHookWithTelemetry(cfg, store, svc, nil)
}

// BuildPostAppendHookWithTelemetry is the telemetry-aware chain builder.
func BuildPostAppendHookWithTelemetry(cfg Config, store TurnStore, svc CompressionServices, tel *Telemetry) PostAppendHook {
	if !cfg.Compression.Enabled {
		return nil
	}
	cs, ok := store.(CompressionStore)
	if !ok || cs == nil {
		return nil
	}
	if err := svc.validateFor(cfg); err != nil {
		return nil
	}
	submitStage := NewPostEgressSubmitStageWithTelemetry(cfg, cs, svc, tel)
	egressStage := NewPostReservationEgressStageWithTelemetry(cfg, cs, svc, submitStage, tel)
	return NewCompressionReservationHookWithTelemetry(cfg, cs, egressStage, tel)
}

// BuildPostAppendHookWithNext allows tests and future stages to inject the next stage for chaining.
// This is the reservation-level injection (pre-egress) retained for 4.2-era tests.
func BuildPostAppendHookWithNext(cfg Config, store TurnStore, svc CompressionServices, next PostReservationStage) PostAppendHook {
	if !cfg.Compression.Enabled {
		return nil
	}
	cs, ok := store.(CompressionStore)
	if !ok || cs == nil {
		return nil
	}
	if err := svc.validateFor(cfg); err != nil {
		return nil
	}
	return NewCompressionReservationHook(cfg, cs, next)
}

// BuildPostAppendHookWithEgressNext allows tests and 4.4 to inject the post-egress
// next stage that receives PreparedReservation (sanitized segments + decision).
func BuildPostAppendHookWithEgressNext(cfg Config, store TurnStore, svc CompressionServices, next PostEgressStage) PostAppendHook {
	if !cfg.Compression.Enabled {
		return nil
	}
	cs, ok := store.(CompressionStore)
	if !ok || cs == nil {
		return nil
	}
	if err := svc.validateFor(cfg); err != nil {
		return nil
	}
	egressStage := NewPostReservationEgressStage(cfg, cs, svc, next)
	return NewCompressionReservationHook(cfg, cs, egressStage)
}
