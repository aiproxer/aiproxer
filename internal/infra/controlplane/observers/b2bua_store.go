package observers

import (
	"context"
	"strconv"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// B2BUAStoreDecorator decorates an existing [b2bua.Store] so that attempt
// lineage records are projected into the control-plane recorder while B2BUA
// allocation and continuity semantics stay with the delegate (design "Source
// Event Mapping"; task 4.4, requirements 1.3, 2.2, 3.1, 3.2, 3.3, 3.7, 5.1, 5.3,
// 8.3, 8.5, 10.7).
//
// Behavior:
//   - Only [b2bua.Store.RecordAttempt] records a control-plane attempt event.
//     A-leg allocation, B-leg allocation, weighted-first, and load operations
//     are pure pass-through; they never record and never change continuity
//     semantics (requirement 8.3).
//   - Recording happens after the delegate succeeds and is always best-effort
//     via [controlplane.RecorderService.RecordBestEffort]. Control-plane
//     failures degrade status only and never change attempt routing outcomes
//     or attempt replacement behavior (requirement 5.1, 5.3, 10.7).
//   - A-leg/B-leg lineage, attempt sequence, route outcome, and surfaced/
//     swallowed/failed/cancelled states are projected so query consumers can
//     distinguish surfaced attempts from non-surfaced attempts (requirement 3.2,
//     3.3).
//   - Source event key is `b2bua-attempt:{a_leg_id}:{b_leg_id}:{seq}`,
//     deterministic and never hashing raw payloads, headers, or tokens.
//   - When the control-plane capability is disabled, the adapter is a no-op.
//
// The decorator performs no I/O beyond the recorder call and starts no
// goroutines.
type B2BUAStoreDecorator struct {
	delegate   b2bua.Store
	normalizer *controlplane.Normalizer
	recorder   *controlplane.RecorderService
}

// B2BUAStoreDecoratorConfig configures a [B2BUAStoreDecorator].
type B2BUAStoreDecoratorConfig struct {
	Delegate   b2bua.Store
	Normalizer *controlplane.Normalizer
	// Recorder appends control-plane events. It is concrete because this decorator
	// requires RecordBestEffort; may be nil to disable recording.
	Recorder *controlplane.RecorderService
}

// NewB2BUAStoreDecorator returns a decorator that wraps delegate.
func NewB2BUAStoreDecorator(cfg B2BUAStoreDecoratorConfig) *B2BUAStoreDecorator {
	return &B2BUAStoreDecorator{
		delegate:   cfg.Delegate,
		normalizer: cfg.Normalizer,
		recorder:   cfg.Recorder,
	}
}

// SetALegRetirementObserver forwards the optional lifecycle seam without
// changing control-plane recording behavior.
func (d *B2BUAStoreDecorator) SetALegRetirementObserver(observer func(string)) {
	if delegate, ok := d.delegate.(b2bua.ALegRetirementObserver); ok {
		delegate.SetALegRetirementObserver(observer)
	}
}

// SetKeyedALegRetirementObserver forwards the optional keyed lifecycle seam
// without changing control-plane recording behavior.
func (d *B2BUAStoreDecorator) SetKeyedALegRetirementObserver(key string, observer func(string)) {
	if delegate, ok := d.delegate.(b2bua.ALegRetirementObservers); ok {
		delegate.SetKeyedALegRetirementObserver(key, observer)
	}
}

// ResolveALeg is a pass-through.
func (d *B2BUAStoreDecorator) ResolveALeg(ctx context.Context, continuityKey string) (b2bua.ALegRecord, error) {
	return d.delegate.ResolveALeg(ctx, continuityKey)
}

// CreateALeg is a pass-through.
func (d *B2BUAStoreDecorator) CreateALeg(ctx context.Context, continuityKey string) (b2bua.ALegRecord, error) {
	return d.delegate.CreateALeg(ctx, continuityKey)
}

// FetchALeg is a pass-through.
func (d *B2BUAStoreDecorator) FetchALeg(ctx context.Context, aLegID string) (b2bua.ALegRecord, error) {
	return d.delegate.FetchALeg(ctx, aLegID)
}

// SetWeightedFirstConsumed is a pass-through.
func (d *B2BUAStoreDecorator) SetWeightedFirstConsumed(ctx context.Context, aLegID string, consumed bool) error {
	return d.delegate.SetWeightedFirstConsumed(ctx, aLegID, consumed)
}

// NextBLeg is a pass-through.
func (d *B2BUAStoreDecorator) NextBLeg(ctx context.Context, aLegID string) (b2bua.BLegRecord, error) {
	return d.delegate.NextBLeg(ctx, aLegID)
}

// WithBLegAllocationAuthority forwards the underlying memory authority without
// invoking recorders under its lock. Unsupported delegates fail explicitly.
func (d *B2BUAStoreDecorator) WithBLegAllocationAuthority(ctx context.Context, aLegID string, apply func(bool) error) error {
	if authority, ok := d.delegate.(b2bua.BLegAllocationAuthority); ok {
		return authority.WithBLegAllocationAuthority(ctx, aLegID, apply)
	}
	return b2bua.ErrBLegAllocationAuthorityUnsupported
}

// RecordAttempt delegates and records an attempt-lineage event (best-effort).
// Control-plane failures never change routing outcomes or attempt replacement.
func (d *B2BUAStoreDecorator) RecordAttempt(ctx context.Context, rec aipapi.AttemptRecord) error {
	if err := d.delegate.RecordAttempt(ctx, rec); err != nil {
		return err
	}
	if d.recorder == nil || d.normalizer == nil {
		return nil
	}
	surfaced, outcome := mapB2BUAAttemptOutcome(rec.Outcome)
	src := controlplane.AttemptSourceRecord{
		SourceEventKey: "b2bua-attempt:" + rec.ALegID + ":" + rec.BLegID + ":" + strconv.Itoa(rec.Seq),
		OccurredAt:     rec.FinishedAt,
		ALegID:         rec.ALegID,
		BLegID:         rec.BLegID,
		AttemptSeq:     rec.Seq,
		BackendID:      rec.BackendID,
		Model:          rec.EffectiveModel,
		Surfaced:       surfaced,
		Outcome:        outcome,
		ErrorClass:     rec.Reason,
		StartedAt:      rec.StartedAt,
		FinishedAt:     rec.FinishedAt,
	}
	ev, err := d.normalizer.FromAttempt(src)
	if err != nil {
		return nil
	}
	_, recErr := d.recorder.RecordBestEffort(ctx, ev)
	ignoreBestEffortRecorderErr(recErr)
	return nil
}

// LoadAttempts is a pass-through read.
func (d *B2BUAStoreDecorator) LoadAttempts(ctx context.Context, aLegID string) ([]aipapi.AttemptRecord, error) {
	return d.delegate.LoadAttempts(ctx, aLegID)
}

// SetInterleavedState delegates interleaved-thinking persistence to the
// underlying store when it implements [b2bua.InterleavedStateStore]. The
// decorator never records this path (it carries no attempt lineage); it only
// preserves the optional capability so the executor's InterleavedStateStore
// assertion still succeeds when control-plane is enabled. When the delegate
// does not implement the contract, the decorator mirrors the executor's
// graceful behavior: empty state is a no-op and non-empty state fails closed
// with [b2bua.ErrInterleavedStateUnsupported].
func (d *B2BUAStoreDecorator) SetInterleavedState(ctx context.Context, aLegID string, state interleavedstate.State) error {
	if is, ok := d.delegate.(b2bua.InterleavedStateStore); ok && is != nil {
		return is.SetInterleavedState(ctx, aLegID, state)
	}
	if state.IsEmpty() {
		return nil
	}
	return b2bua.ErrInterleavedStateUnsupported
}

// FetchInterleavedState delegates interleaved-thinking state load to the
// underlying store when it implements [b2bua.InterleavedStateStore]. When the
// delegate does not implement the contract, the decorator returns a zero state
// and nil so callers treat the A-leg as a new session, matching the executor's
// graceful behavior for stores that lack the optional capability.
func (d *B2BUAStoreDecorator) FetchInterleavedState(ctx context.Context, aLegID string) (interleavedstate.State, error) {
	if is, ok := d.delegate.(b2bua.InterleavedStateStore); ok && is != nil {
		return is.FetchInterleavedState(ctx, aLegID)
	}
	return interleavedstate.State{}, nil
}

// mapB2BUAAttemptOutcome maps a [aipapi.AttemptOutcome] to control-plane
// surfaced/outcome kinds (requirement 3.2, 3.3). SurfacedFailure and Success
// produced client-visible output; SwallowedFailure and Cancelled did not. Keep
// this separate from the secure-session mapper: the source enums encode
// different state machines.
func mapB2BUAAttemptOutcome(o aipapi.AttemptOutcome) (cp.AttemptSurfaced, cp.AttemptOutcome) {
	switch o {
	case aipapi.AttemptSuccess:
		return cp.AttemptSurfacedSurfaced, cp.AttemptOutcomeSucceeded
	case aipapi.AttemptSurfacedFailure:
		return cp.AttemptSurfacedSurfaced, cp.AttemptOutcomeFailed
	case aipapi.AttemptSwallowedFailure:
		return cp.AttemptSurfacedSwallowed, cp.AttemptOutcomeFailed
	case aipapi.AttemptCancelled:
		return cp.AttemptSurfacedSwallowed, cp.AttemptOutcomeCancelled
	}
	return cp.AttemptSurfacedUnknown, cp.AttemptOutcomeUnknown
}

// Unwrap returns the underlying b2bua.Store delegate.
func (b *B2BUAStoreDecorator) Unwrap() any {
	if b == nil {
		return nil
	}
	return b.delegate
}

var (
	_ b2bua.Store                  = (*B2BUAStoreDecorator)(nil)
	_ b2bua.InterleavedStateStore  = (*B2BUAStoreDecorator)(nil)
	_ b2bua.ALegRetirementObserver = (*B2BUAStoreDecorator)(nil)
)
