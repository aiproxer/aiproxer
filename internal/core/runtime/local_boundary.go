package runtime

import (
	"strings"
	"time"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// localBoundaryCaptureEnabled reports whether this executor has a local
// observation consumer. The accumulator is request/B-leg scoped and is never
// allocated for ordinary no-accounting execution.
func (e *Executor) localBoundaryCaptureEnabled() bool {
	if e == nil {
		return false
	}
	return e.MeteringRecorder != nil || atomicObservationSinkConfigured(e.MeteringObservationSink) || e.BillingLegObserver != nil || e.TerminalUsageSink != nil
}

// localBoundaryCaptureRequired reports whether the canonical path must retain
// final-boundary evidence before a provider fast lane may commit bytes.
func (e *Executor) localBoundaryCaptureRequired() bool {
	// The durable Phase 3 recorder and V2 observation sink are explicit required
	// local-evidence capabilities. Terminal billing sinks remain compatible with
	// the wire lane; their attempt-owned records receive local observations when
	// available, while the optional observer/no-op test sink must not alter
	// eligibility.
	return e != nil && (e.MeteringRecorder != nil || atomicObservationSinkConfigured(e.MeteringObservationSink))
}

func atomicObservationSinkConfigured(sink metering.ObservationSink) bool {
	if sink == nil {
		return false
	}
	_, ok := sink.(metering.AtomicObservationSink)
	return ok
}

func (e *Executor) newLocalBoundaryAccumulator() *coremetering.BoundaryAccumulator {
	if !e.localBoundaryCaptureEnabled() {
		return nil
	}
	return coremetering.NewBoundaryAccumulator()
}

func (a *attemptSession) drainLocalBoundaryObservations(now time.Time) []metering.Observation {
	if a == nil {
		return nil
	}
	return a.localBoundaryObservations(now, true)
}

// checkpointLocalBoundaryObservations snapshots the bounded local boundary
// planes without retiring them from the terminal billing record. Each changed
// cumulative snapshot receives the next revision and joins the same durable
// checkpoint queue as provider observations.
func (a *attemptSession) checkpointLocalBoundaryObservations(now time.Time) {
	if a == nil || !atomicObservationSinkConfigured(a.observationSink) {
		return
	}
	a.localBoundaryObservations(now, false)
}

func (a *attemptSession) localBoundaryObservations(now time.Time, drain bool) []metering.Observation {
	a.billingMu.Lock()
	if a.boundaryDrained && !drain {
		a.billingMu.Unlock()
		return nil
	}
	if drain && a.boundaryDrained {
		a.billingMu.Unlock()
		return nil
	}
	if drain {
		a.boundaryDrained = true
	}
	boundary := a.boundary
	if boundary == nil {
		// Ordinary no-accounting execution never allocates an accumulator, so
		// do not build the observation identity (scope clone) at all.
		a.billingMu.Unlock()
		return nil
	}
	identity := coremetering.ObservationIdentity{
		StoreID:       strings.TrimSpace(a.billingStoreID),
		RequestID:     strings.TrimSpace(a.requestID),
		CallID:        a.billingCallID.String(),
		BillingCallID: a.billingCallID.String(),
		ALegID:        strings.TrimSpace(a.bleg.ALegID),
		BLegID:        strings.TrimSpace(a.bleg.BLegID),
		AttemptID:     strings.TrimSpace(a.bleg.BLegID),
		AttemptSeq:    uint64(maxInt(a.bleg.Seq, 0)),
		Scope:         a.boundaryScope.Clone(),
		ObservedAt:    now,
		ReceivedAt:    now,
	}
	a.billingMu.Unlock()
	return a.versionLocalBoundaryObservations(boundary.Observations(identity), drain)
}

func (a *attemptSession) observeLocalProviderEvent(event aipapi.Event, media ...coremetering.MediaSummary) {
	if a != nil && a.boundary != nil {
		a.boundary.ObserveProviderEvent(event, media...)
	}
}

func (a *attemptSession) observeLocalCustomerEvent(event aipapi.Event, media ...coremetering.MediaSummary) {
	if a != nil && a.boundary != nil {
		a.boundary.ObserveCustomerEvent(event, media...)
	}
}

func maxInt(value, floor int) int {
	if value < floor {
		return floor
	}
	return value
}
