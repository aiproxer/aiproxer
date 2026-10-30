package extensions

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestFinalStreamObservationWithoutObserversIsAllocationFree is a
// shared-hot-path allocation ratchet. RunFinalStreamObservationStage runs once
// per delivered event and the attempt always owns a session, while the stream
// observers exist only when a feature contributes them. Capturing the
// 640-byte canonical event in the observer closure moved it to the heap on
// every delivered event, including a nil session and a session whose observers
// have already finished (or were never opened): both return before any
// observer runs, and neither may pay for an event copy.
//
//nolint:paralleltest // AllocsPerRun reads process-wide allocation counters.
func TestFinalStreamObservationWithoutObserversIsAllocationFree(t *testing.T) {
	ctx := context.Background()
	ev := aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "delta"}

	for name, session := range map[string]*FinalStreamObservationSession{
		"nil session":  nil,
		"no observers": {},
	} {
		t.Run(name, func(t *testing.T) {
			allocs := testing.AllocsPerRun(200, func() {
				if err := RunFinalStreamObservationStage(ctx, nil, nil, session, ev, false); err != nil {
					t.Fatalf("RunFinalStreamObservationStage: %v", err)
				}
			})
			if allocs != 0 {
				t.Fatalf("final stream observation stage allocated %.2f objects per delivered event without observers, want 0", allocs)
			}
		})
	}
}
