package openresponses

import (
	"context"
	"errors"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

// observedEventStream records canonical events without changing executor or
// transport commitment semantics.
type observedEventStream struct {
	aipapi.EventStream
	observer aipcont.StreamObserver
}

func (s *observedEventStream) Recv(ctx context.Context) (aipapi.Event, error) {
	event, err := s.EventStream.Recv(ctx)
	if err == nil {
		safeObserveFrontend(ctx, s.observer, event)
	}
	return event, err
}

func (s *observedEventStream) Close() error {
	safeCloseObserverFrontend(s.observer)
	return s.EventStream.Close()
}

// continuationReservationOwner is the private lifecycle contract for an
// observer that owns reservation cleanup. FinalizeIncomplete is accepted only
// together with the ownership marker, so a custom observer cannot accidentally
// finalize while the frontend still believes it owns the reservation.
//
// Implementations must detach/consume their reservation-cleanup ownership before
// invoking the underlying callback, and must consume it at most once on both
// successful and failed finalization. The frontend deliberately does not perform
// a fallback delete after this capability reports consumption; this prevents a
// failed recorder write from causing a second delete after the recorder/store
// has already cleaned up the reservation.
type continuationReservationOwner interface {
	OwnsContinuationReservation() bool
	ContinuationReservationCleanupConsumed() bool
	FinalizeIncomplete(context.Context) error
	ReleaseContinuationReservation()
}

func safeReleaseContinuationReservation(owner continuationReservationOwner) {
	if owner == nil || !safeOwnsContinuationReservation(owner) {
		return
	}
	defer func() { _ = recover() }()
	owner.ReleaseContinuationReservation()
}

func safeOwnsContinuationReservation(owner continuationReservationOwner) (owns bool) {
	if owner == nil {
		return false
	}
	defer func() {
		if recover() != nil {
			owns = false
		}
	}()
	return owner.OwnsContinuationReservation()
}

func safeCleanupConsumed(owner continuationReservationOwner) (consumed bool) {
	if owner == nil {
		return true
	}
	defer func() {
		if recover() != nil {
			consumed = false
		}
	}()
	return owner.ContinuationReservationCleanupConsumed()
}

func safeFinalizeIncomplete(ctx context.Context, owner continuationReservationOwner) (err error) {
	if owner == nil || !safeOwnsContinuationReservation(owner) {
		return errors.New("openresponses: observer does not own continuation reservation")
	}
	defer func() {
		if recover() != nil {
			err = errors.New("openresponses: observer finalization panicked")
		}
	}()
	return owner.FinalizeIncomplete(ctx)
}

// safeObserveFrontend runs observer callbacks outside the client stream
// contract: a recording panic must never abort a delivered response. Mirrors
// the core continuationObservedStream isolation seam.
func safeObserveFrontend(ctx context.Context, observer aipcont.StreamObserver, event aipapi.Event) {
	if observer == nil {
		return
	}
	defer func() { _ = recover() }()
	observer.Observe(ctx, event)
}

func safeCloseObserverFrontend(observer aipcont.StreamObserver) {
	if observer == nil {
		return
	}
	defer func() { _ = recover() }()
	_ = observer.Close()
}

var _ aipapi.EventStream = (*observedEventStream)(nil)
