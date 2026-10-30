package continuation

import (
	"context"
	"math"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type overflowBoundaryRecorder struct{ calls int }

func (r *overflowBoundaryRecorder) RecordTerminal(context.Context, ContinuationRecord) error {
	r.calls++
	return nil
}

func TestStreamRecorderEventBytesInt64Boundary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		eventBytes   int64
		delta        string
		wantOverflow bool
	}{
		// fitsExactly seeds the counter so the observed event lands exactly on
		// math.MaxInt64. The seed derives from the event's measured size so the
		// boundary intent survives PayloadBytes counting every retained field.
		{name: "fitsExactly", eventBytes: math.MaxInt64 - int64(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "hello"}.PayloadBytes()), delta: "hello", wantOverflow: false},
		{name: "overflowsByOne", eventBytes: math.MaxInt64, delta: "x", wantOverflow: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			backend := &overflowBoundaryRecorder{}
			cleanupCalls := 0
			r := NewStreamRecorder(backend, ContinuationRecord{}, func() { cleanupCalls++ })
			r.eventBytes = tt.eventBytes

			r.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: tt.delta})

			if tt.wantOverflow {
				if !r.overflow {
					t.Fatalf("expected overflow, got eventBytes=%d events=%d", r.eventBytes, len(r.events))
				}
				if cleanupCalls != 1 {
					t.Fatalf("overflow cleanup calls=%d, want 1", cleanupCalls)
				}
				if len(r.events) != 0 {
					t.Fatalf("overflow recorded %d events, want 0", len(r.events))
				}
				if r.eventBytes != tt.eventBytes {
					t.Fatalf("overflow wrapped eventBytes to %d, want unchanged %d", r.eventBytes, tt.eventBytes)
				}
				return
			}
			if r.overflow {
				t.Fatalf("unexpected overflow at exact int64 boundary")
			}
			if len(r.events) != 1 || r.eventBytes != math.MaxInt64 {
				t.Fatalf("boundary event not recorded: events=%d eventBytes=%d", len(r.events), r.eventBytes)
			}
			if cleanupCalls != 0 {
				t.Fatalf("cleanup released without overflow: %d", cleanupCalls)
			}
			// A further event must now trip the int64 overflow guard.
			r.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"})
			if !r.overflow || cleanupCalls != 1 {
				t.Fatalf("post-boundary event did not overflow: overflow=%v cleanup=%d", r.overflow, cleanupCalls)
			}
		})
	}
}

func TestStreamRecorderMaxRecordBytesStillOverflows(t *testing.T) {
	t.Parallel()
	backend := &overflowBoundaryRecorder{}
	cleanupCalls := 0
	r := NewStreamRecorder(backend, ContinuationRecord{
		Policy: StoragePolicy{Limits: StorageLimits{MaxRecordBytes: 4}},
	}, func() { cleanupCalls++ })
	r.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "too large"})
	if !r.overflow {
		t.Fatalf("expected quota overflow, got eventBytes=%d events=%d", r.eventBytes, len(r.events))
	}
	if cleanupCalls != 1 {
		t.Fatalf("quota overflow cleanup calls=%d, want 1", cleanupCalls)
	}
	if len(r.events) != 0 {
		t.Fatalf("quota overflow recorded %d events, want 0", len(r.events))
	}
}
