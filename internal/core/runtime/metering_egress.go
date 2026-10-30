package runtime

import (
	"context"
	"strconv"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/internal/core/metering/plane"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func quantitiesFromUsageEvent(ev aipapi.Event) []metering.Quantity {
	return plane.QuantitiesFromUsageEvent(ev)
}

func moneyFromUsageEvent(ev aipapi.Event) *metering.MoneyObservation {
	return plane.MoneyFromUsageEvent(ev)
}

// emitBackendEgressMeteringFact appends a backend-egress fact when a freeze exists
// for the B-leg (requirements 2.3, 5.3). Nil recorder / missing freeze are no-ops.
func (e *Executor) emitBackendEgressMeteringFact(
	ctx context.Context,
	blegID string,
	outcome metering.AttemptOutcome,
	surfaced metering.SurfacedState,
	usageEv aipapi.Event,
) {
	if e == nil {
		return
	}
	holder := meteringHolderFrom(ctx)
	if holder == nil {
		return
	}
	beSnap := holder.BackendIngressFor(blegID)
	if beSnap == nil {
		return
	}
	seq := holder.NextSequence()
	fact, err := checkpoint.FactFromEgress(checkpoint.EgressFactInput{
		Checkpoint: checkpoint.BackendEgressCheckpoint(*beSnap, outcome, surfaced),
		FactID:     "be-egress:" + strings.TrimSpace(blegID) + ":" + strconv.FormatInt(seq, 10),
		Sequence:   seq,
		Quantities: quantitiesFromUsageEvent(usageEv),
		Outcome:    outcome,
		Surfaced:   surfaced,
		Money:      moneyFromUsageEvent(usageEv),
		Now:        e.now(),
	})
	if err != nil {
		if e.Log != nil {
			e.Log.DebugContext(ctx, "metering backend egress fact", "error", err)
		}
		return
	}
	if err := e.appendMeteringFact(ctx, fact); err != nil && e.Log != nil {
		e.Log.DebugContext(ctx, "metering recorder append", "error", err)
	}
}

// emitFrontendEgressMeteringFact appends a frontend-egress fact when FE ingress exists
// (requirement 2.4). Safe to call from winner finalize, cancel-after-output, and
// client-visible error terminals. Returns the emitted fact for request-authority
// settlement (requirement 4.2).
func (e *Executor) emitFrontendEgressMeteringFact(ctx context.Context, traceID string, usageEv aipapi.Event) (metering.Fact, bool) {
	if e == nil {
		return metering.Fact{}, false
	}
	holder := meteringHolderFrom(ctx)
	if holder == nil || holder.FrontendIngress == nil {
		return metering.Fact{}, false
	}
	customerEv := customerPlaneUsageEvent(usageEv)
	if customerEv.Kind == "" && usageEv.Kind == aipapi.EventUsageDelta {
		// Provider-only evidence still requires a customer FE terminal fact with
		// quantities omitted rather than importing provider counters/money.
		customerEv = aipapi.Event{Kind: aipapi.EventUsageDelta}
	}
	seq := holder.NextSequence()
	fact, err := checkpoint.FactFromEgress(checkpoint.EgressFactInput{
		Checkpoint: checkpoint.FrontendEgressCheckpoint(*holder.FrontendIngress),
		FactID:     "fe-egress:" + strings.TrimSpace(traceID) + ":" + strconv.FormatInt(seq, 10),
		Sequence:   seq,
		Quantities: quantitiesFromUsageEvent(customerEv),
		Money:      nil,
		Now:        e.now(),
	})
	if err != nil {
		if e.Log != nil {
			e.Log.DebugContext(ctx, "metering frontend egress fact", "error", err)
		}
		return metering.Fact{}, false
	}
	if err := e.appendMeteringFact(ctx, fact); err != nil {
		if e.Log != nil {
			e.Log.DebugContext(ctx, "metering recorder append", "error", err)
		}
		return metering.Fact{}, false
	}
	return fact, true
}

func (t *turnTerminal) emitBackendEgressMeteringFactForAttempt(
	ctx context.Context,
	attempt *attemptSession,
	outcome metering.AttemptOutcome,
	surfaced metering.SurfacedState,
	usageEv aipapi.Event,
) {
	if t == nil || t.emitBackendEgress == nil || attempt == nil {
		return
	}
	t.emitBackendEgress(ctx, attempt.bleg.BLegID, outcome, surfaced, usageEv)
}

func (t *turnTerminal) emitFrontendEgressMeteringFact(ctx context.Context, traceID string, usageEv aipapi.Event) (metering.Fact, bool) {
	if t == nil || t.emitFrontendEgress == nil {
		return metering.Fact{}, false
	}
	return t.emitFrontendEgress(ctx, traceID, usageEv)
}
