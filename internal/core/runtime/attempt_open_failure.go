package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// nilStreamOpenReason is the upstream-failure reason for a backend Open that
// returned no stream and reported no error.
//
// A backend that answers an open call with nothing to serve opened no stream:
// there is no output to publish, no provider evidence to bind and no attempt
// to attribute to that backend. The core treats such an open as a failed open,
// so the attempt is never recorded as a successful open.
const nilStreamOpenReason = "backend open returned no stream"

// nilStreamOpenFailure is the upstream failure for a backend Open that
// succeeded without producing a stream.
//
// It is a pre-output upstream failure: nothing reached the client, so the core
// fails over to the next route candidate instead of spending the rest of the
// attempt budget on a backend that opened nothing. The candidate key travels
// with it, so the attempt record and the terminal error name the backend that
// opened nothing.
func nilStreamOpenFailure(candidateKey string) error {
	return &aipapi.UpstreamFailureError{
		Phase:        aipapi.PhasePreOutput,
		Recoverable:  true,
		Reason:       nilStreamOpenReason,
		CandidateKey: candidateKey,
	}
}

// IsNilStreamOpenError reports whether err is or wraps the upstream failure
// for a backend Open that returned no stream.
//
// The frontend wire mapping keys on this predicate so it does not have to
// know the failure's shape: only the reason this file owns identifies it.
func IsNilStreamOpenError(err error) bool {
	if err == nil {
		return false
	}
	var uf *aipapi.UpstreamFailureError
	if !errors.As(err, &uf) {
		return false
	}
	return uf != nil && uf.Reason == nilStreamOpenReason &&
		uf.Phase == aipapi.PhasePreOutput && uf.Recoverable
}

// contextErr returns ctx.Err() for a context that may be nil.
func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// nilManagedStream reports whether a backend Open returned no usable stream.
//
// It rejects an untyped nil interface and a typed-nil implementation (a nil
// pointer boxed in a non-nil interface): neither can serve a receive, and a
// typed nil would otherwise reach the successful-open commit and the receive
// pipeline as if it were a real stream.
func nilManagedStream(stream aipapi.ManagedEventStream) bool {
	if stream == nil {
		return true
	}
	value := reflect.ValueOf(stream)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// classifyNilStreamOpen classifies a backend Open that returned no usable
// stream.
//
// A canceled or expired open is not a backend fault for the flow: the adapter
// returned nothing because its context was already done, so the cancellation
// or the deadline owns the attempt.
//
// The upstream failure travels beside the classified error, because the
// backend still opened no stream: the route must report that fault even when
// the classified error is a cancellation.
func classifyNilStreamOpen(openCtx context.Context, upstream error) error {
	if openCtx != nil {
		if ctxErr := openCtx.Err(); ctxErr != nil {
			return ctxErr
		}
	}
	return upstream
}

// openFailure is one failed backend Open on either payload path.
type openFailure struct {
	err error
	// ttftExpired reports that the attempt's TTFT deadline caused the failure;
	// each payload path owns that judgement.
	ttftExpired bool
	ttftScope   ttftTimeoutScope
	// openErr is the cancellation provenance: the error of the context the
	// Open ran under, captured at the acquisition site before any local
	// cleanup cancels that context. A non-nil value means the request, the
	// A-leg or the core's own budget ended the attempt, so the backend is not
	// at fault; it covers client cancellation and request deadlines alike.
	openErr error
	// wire selects the large-body payload rules documented in classify.
	wire bool
	// postOpen is the raw acquisition fact: the backend Open call returned
	// without an error. The backend was contacted, so the provider may have
	// processed the request and the leg failed rather than never started -
	// whatever the classified error says, and even when the failure is
	// swallowed for failover. Never-started is an authoritative known-zero
	// verdict on the customer and the provider side, and the core cannot
	// support it for an open the backend answered.
	postOpen bool
	// bindFailed marks a failure after a successful acquisition: the backend
	// opened a stream and the economic evidence binder then failed, so the
	// attempt is not a failed open.
	bindFailed bool
	// nilStreamErr is the typed upstream failure for an open that returned no
	// usable stream. It is kept apart from err because a canceled or expired
	// open is still a backend that opened no stream, and the route must
	// report that fault rather than the cancellation that ended it.
	nilStreamErr error
}

// openFailureVerdict is how one failed Open is recorded and rolled back.
type openFailureVerdict struct {
	recordOutcome aipapi.AttemptOutcome
	recordReason  string
	releaseKind   usagelimit.ReleaseKind
	legOutcome    usageevidence.LegOutcome
	// raceLoser marks a parallel leg canceled by a sibling's win: the backend
	// never counts as attempted.
	raceLoser bool
}

// failOpen records and rolls back one failed backend Open. It returns nil
// when the failure is swallowed so the next candidate can run, and the
// surfaced error otherwise. Both payload paths share this verdict; the rules
// that differ between them live in classify.
func (tx *attemptTx) failOpen(ctx context.Context, c routing.AttemptCandidate, plan candidatePlan, f openFailure, openSpan trace.Span) error {
	e := tx.e
	if f.nilStreamErr != nil && tx.failures != nil {
		// A backend that opened no stream is a backend fault, not a transient
		// upstream blip. Remember it so an exhausted route reports the real
		// cause instead of a routing exhaustion, and tell the transform
		// aggregate that a candidate was contacted and executed, so it cannot
		// report that every candidate was excluded.
		tx.failures.SwallowedUpstreamFailure = f.nilStreamErr
		tx.failures.TransformExcludes.noteOther()
	}
	if f.ttftExpired {
		tf := ttftFailure(f.ttftScope, c.Key)
		legOutcome := usageevidence.LegOutcomeNeverStarted
		if f.postOpen {
			// The Open call returned without an error, so the backend was
			// contacted and the provider may have processed the request: the
			// leg failed rather than never started.
			legOutcome = usageevidence.LegOutcomeFailed
		}
		if f.ttftScope == ttftTimeoutLeaf {
			tx.recordOpenFailure(ctx, f.bindFailed, aipapi.AttemptSwallowedFailure, ttftAttemptReason(f.ttftScope), tf)
			tx.rollbackSimple(ctx, sdkterminal.CommandBackendOpenFailure, usagelimit.ReleaseKindSwallowed, legOutcome, nil, "")
			return nil
		}
		tx.recordOpenFailure(ctx, f.bindFailed, aipapi.AttemptSurfacedFailure, ttftAttemptReason(f.ttftScope), tf)
		tx.rollbackSimple(ctx, sdkterminal.CommandBackendOpenFailure, usagelimit.ReleaseKindLosing, legOutcome, nil, "")
		return fmt.Errorf("executor: backend open %q: %w", c.Primary.Backend, aipapi.ErrTTFTTimeout)
	}
	openSpan.RecordError(f.err)
	openSpan.SetStatus(codes.Error, "backend open failed")
	if aipapi.IsRecoverablePreOutput(f.err) {
		if plan.stickyBinding && c.Primary.Backend == plan.stickyBackendID {
			if plan.parallel {
				if tx.failures != nil {
					tx.failures.AffinityReset = "recoverable_pre_output_open"
				}
			} else {
				e.clearAffinityBinding(ctx, tx.reqFacts.traceID, tx.routeFacts.affinityKey, tx.routeFacts.affinitySet, "recoverable_pre_output_open")
			}
		}
		reason := "recoverable pre-output (open)"
		if f.nilStreamErr != nil {
			reason = nilStreamOpenReason
		}
		tx.recordOpenFailure(ctx, f.bindFailed, aipapi.AttemptSwallowedFailure, reason, f.err)
		if tx.failures != nil && errors.Is(f.err, aipapi.ErrCapabilityReject) {
			// A swallowed Open failure that is capability-classified names a
			// request this candidate cannot serve. Keep it for the terminal
			// error so an exhausted route surfaces the reason instead of a
			// generic planning error. Transient swallowed failures are
			// deliberately not recorded: their terminal mapping stays as it
			// is today.
			tx.failures.SwallowedCapabilityReject = f.err
		}
		diag.LogDecision(
			ctx, e.Log, "recoverable_pre_output_swallowed",
			diag.AttrOpts{CallID: tx.reqFacts.traceID, BLegID: tx.bleg.BLegID},
			slog.String("candidate_key", c.Key),
			slog.String("phase", "open"),
		)
		swallowedLeg := usageevidence.LegOutcomeNeverStarted
		if f.postOpen {
			// The Open call returned without an error, so the backend was
			// contacted and the provider may have processed the request: the
			// leg failed rather than never starting, on both payload paths.
			swallowedLeg = usageevidence.LegOutcomeFailed
		}
		tx.rollbackSimple(ctx, sdkterminal.CommandBackendOpenFailure, usagelimit.ReleaseKindSwallowed, swallowedLeg, f.err, reason)
		return nil
	}
	v := f.classify(plan)
	if v.raceLoser {
		tx.backendAttempted = false
		if !f.wire {
			tx.authLifecycle.backendAttempted.Store(false)
		}
	}
	tx.recordOpenFailure(ctx, f.bindFailed, v.recordOutcome, v.recordReason, f.err)
	tx.rollbackSimple(ctx, sdkterminal.CommandBackendOpenFailure, v.releaseKind, v.legOutcome, nil, "")
	return fmt.Errorf("executor: backend open %q: %w", c.Primary.Backend, f.err)
}

// classify maps a surfaced (non-TTFT, non-recoverable) Open failure to its
// record and rollback. The payload paths differ, and the differences are
// stated here rather than in two copies:
//   - ordinary: a parallel leg is a race loser when the error or its open
//     context is canceled; its billing leg never started.
//   - wire: a parallel leg is a race loser only when the error is canceled;
//     its billing leg is canceled, and any other parallel failure is failed.
func (f openFailure) classify(plan candidatePlan) openFailureVerdict {
	v := openFailureVerdict{
		recordOutcome: aipapi.AttemptSurfacedFailure,
		recordReason:  attemptReasonDetail(f.err),
		releaseKind:   usagelimit.ReleaseKindLosing,
		legOutcome:    usageevidence.LegOutcomeNeverStarted,
	}
	if f.postOpen {
		// The Open call returned without an error, so the backend was
		// contacted and the provider may have processed the request. The leg
		// failed rather than never started: never-started is an authoritative
		// known-zero verdict on the customer and the provider side, and the
		// core cannot support it for an open the backend answered.
		v.legOutcome = usageevidence.LegOutcomeFailed
	}
	canceled := errors.Is(f.err, context.Canceled)
	callerCanceled := f.openErr != nil
	// Cancellation provenance comes from the context the Open ran under, not
	// from the error alone. A caller cancellation and a request deadline end
	// the attempt without the backend being at fault, so they are recorded as
	// cancellations and never count against the backend's health.
	if callerCanceled {
		v.recordOutcome = aipapi.AttemptCancelled
	}
	if canceled && !callerCanceled {
		// The adapter returned a cancellation the caller did not cause: the
		// leg never started, but the backend stays accountable for it.
		v.releaseKind = usagelimit.ReleaseKindAdmissionFailure
		return v
	}
	raceCanceled := canceled || (!f.wire && errors.Is(f.openErr, context.Canceled))
	if plan.parallel && raceCanceled {
		if plan.parentCtx == nil || plan.parentCtx.Err() == nil {
			v.recordOutcome = aipapi.AttemptCancelled
			v.recordReason = "parallel race loser"
			v.releaseKind = usagelimit.ReleaseKindAdmissionFailure
			v.raceLoser = true
			if f.wire {
				v.legOutcome = usageevidence.LegOutcomeCanceled
			}
		}
		return v
	}
	if plan.parallel && f.wire {
		v.legOutcome = usageevidence.LegOutcomeFailed
	}
	return v
}
