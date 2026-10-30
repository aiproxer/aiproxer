package metering

import (
	"context"
	"errors"
	"fmt"
	"sync"

	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// ErrObservationProducerDraining identifies an append that was refused because
// the process already closed observation admission. It is typed so a producer
// can distinguish "the fence is closed" from "the durable sink failed".
var ErrObservationProducerDraining = errors.New("metering: observation producer admission is closed")

// ObservationProducerFence is the process-owned accepted-producer fence over
// one borrowed atomic observation sink.
//
// The fence is the only observation sink a host-owned producer ever sees. It
// admits an append under one mutex, counts it as in flight, and completes it
// only after the borrowed synchronous sink call has fully returned. Drain
// closes admission and waits for the accepted appends to complete, so a caller
// can prove that no producer is still writing into a store it is about to
// close.
//
// The fence never closes the borrowed sink and never opens a resource. The
// borrowed owner keeps the sink's lifetime.
type ObservationProducerFence interface {
	sdkmetering.AtomicObservationSink
	// Drain closes admission and waits for every accepted append to complete.
	// A timeout leaves the admission closed and the same wait retryable.
	Drain(ctx context.Context) error
}

// EconomicObservationProducerFence is the accepted-producer fence that also
// preserves the consumer-owned late-economics capability of the borrowed sink.
// It exists so wrapping a sink never removes the
// AppendEconomicObservationWithOutbox capability the journal's outbox
// transaction provides.
type EconomicObservationProducerFence interface {
	ObservationProducerFence
	EconomicObservationSink
}

// observationAppender is the private process-owned fence implementation. It is
// constructed once per process and shared by every generation of that
// process.
type observationAppender struct {
	mu       sync.Mutex
	draining bool
	inflight int
	drained  chan struct{}
	closed   bool
	sink     sdkmetering.AtomicObservationSink
}

// economicObservationAppender is the fence variant used when the borrowed sink
// also carries the late-economics capability. It embeds the same fence, so the
// admission, in-flight counting, and drain semantics are identical.
type economicObservationAppender struct {
	*observationAppender
	economic EconomicObservationSink
}

var (
	_ ObservationProducerFence               = (*observationAppender)(nil)
	_ EconomicObservationProducerFence       = (*economicObservationAppender)(nil)
	_ sdkmetering.ObservationJournalIdentity = (*observationAppender)(nil)
	_ sdkmetering.ObservationJournalIdentity = (*economicObservationAppender)(nil)
)

// NewObservationProducerFence wraps one borrowed atomic observation sink in
// the process-owned accepted-producer fence. A nil or typed-nil sink returns
// nil: there is nothing to fence, and the caller keeps its existing no-op
// behavior.
func NewObservationProducerFence(sink sdkmetering.AtomicObservationSink) ObservationProducerFence {
	if isNilObservationSink(sink) {
		return nil
	}
	return newObservationAppender(sink)
}

// NewEconomicObservationProducerFence wraps one borrowed atomic observation
// sink that also carries the late-economics capability. It returns nil when
// the borrowed sink does not declare that capability: the fence never invents
// a capability the borrowed owner did not provide.
func NewEconomicObservationProducerFence(sink sdkmetering.AtomicObservationSink) EconomicObservationProducerFence {
	if isNilObservationSink(sink) {
		return nil
	}
	economic, ok := sink.(EconomicObservationSink)
	if !ok || economic == nil {
		return nil
	}
	return &economicObservationAppender{
		observationAppender: newObservationAppender(sink),
		economic:            economic,
	}
}

func newObservationAppender(sink sdkmetering.AtomicObservationSink) *observationAppender {
	return &observationAppender{drained: make(chan struct{}), sink: sink}
}

// ObservationJournalIdentity reports the identity of the borrowed journal. The
// fence is a wrapper over the borrowed sink, so it reports the borrowed
// owner's identity unchanged: wrapping never splits a proven pair.
func (a *observationAppender) ObservationJournalIdentity() sdkmetering.ObservationJournalID {
	if a == nil {
		return ""
	}
	identified, ok := a.sink.(sdkmetering.ObservationJournalIdentity)
	if !ok {
		return ""
	}
	return identified.ObservationJournalIdentity()
}

// isNilObservationSink reports a nil interface or an interface holding a typed
// nil pointer. A typed-nil sink would panic on first use, so it is treated as
// absent at every composition boundary.
func isNilObservationSink(sink sdkmetering.AtomicObservationSink) bool {
	if sink == nil {
		return true
	}
	return isNilLateEconomicSink(sink)
}

// Append admits one observation and forwards it to the borrowed sink. The
// completion of the in-flight count happens only after the borrowed sink call
// has fully returned, so a successful return means the durable write is done.
func (a *observationAppender) Append(ctx context.Context, observation sdkmetering.Observation) error {
	if a == nil {
		return fmt.Errorf("metering: nil observation producer fence")
	}
	if err := a.admit(); err != nil {
		return err
	}
	defer a.complete()
	return a.sink.Append(ctx, observation)
}

// AppendObservations admits one batch and forwards it to the borrowed sink
// under the same admission and completion rules as Append.
func (a *observationAppender) AppendObservations(ctx context.Context, observations []sdkmetering.Observation) error {
	if a == nil {
		return fmt.Errorf("metering: nil observation producer fence")
	}
	if err := a.admit(); err != nil {
		return err
	}
	defer a.complete()
	return a.sink.AppendObservations(ctx, observations)
}

// AppendEconomicObservationWithOutbox forwards one observation through the
// borrowed late-economics capability under the same admission and completion
// rules.
func (a *economicObservationAppender) AppendEconomicObservationWithOutbox(ctx context.Context, observation sdkmetering.Observation) error {
	if a == nil || a.observationAppender == nil {
		return fmt.Errorf("metering: nil observation producer fence")
	}
	if err := a.admit(); err != nil {
		return err
	}
	defer a.complete()
	return a.economic.AppendEconomicObservationWithOutbox(ctx, observation)
}

// admit counts one accepted append. Admission and the draining switch share
// the same mutex, so an append can never be admitted after the cutoff.
func (a *observationAppender) admit() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.draining {
		return fmt.Errorf("%w: the observation producer fence is draining", ErrObservationProducerDraining)
	}
	a.inflight++
	return nil
}

// complete releases one accepted append. The drained channel closes exactly
// once, when the last accepted append completes after admission closed.
func (a *observationAppender) complete() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.inflight--
	if a.draining && a.inflight <= 0 && !a.closed {
		a.closed = true
		close(a.drained)
	}
}

// Drain closes observation admission and waits for every accepted append to
// complete. The wait happens without the mutex held, so an accepted append
// can complete while the drain waits. A timeout leaves the admission closed
// and the same wait channel available for a later retry; the fence never
// reopens admission.
func (a *observationAppender) Drain(ctx context.Context) error {
	if a == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	a.mu.Lock()
	a.draining = true
	if a.inflight <= 0 && !a.closed {
		a.closed = true
		close(a.drained)
	}
	drained := a.drained
	a.mu.Unlock()
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
