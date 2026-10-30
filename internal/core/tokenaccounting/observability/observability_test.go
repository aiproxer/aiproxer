package observability

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewObservationBuildsBoundedSanitizedRecord(t *testing.T) {
	t.Parallel()

	obs, err := NewObservation(Input{
		Labels: Labels{
			Backend:   "openai-primary",
			Model:     "gpt-4.1-mini",
			Plane:     PlaneProviderBillable,
			Source:    SourceProviderCountAPI,
			Authority: AuthorityAuthoritative,
		},
		Status:            StatusUnavailable,
		FallbackReason:    "provider returned bearer sk-live-abc123 in Authorization: Bearer top-secret-token",
		UnavailableReason: "Cookie: sessionid=sensitive; api_key=abc123",
		Err:               errors.New("x-api-key: vendor-secret Authorization: Bearer another-secret"),
		Duration:          25 * time.Millisecond,
		OccurredAt:        time.Unix(10, 0),
	})
	if err != nil {
		t.Fatalf("NewObservation returned error: %v", err)
	}

	if obs.Labels.Backend != "openai-primary" || obs.Labels.Model != "gpt-4.1-mini" {
		t.Fatalf("metadata labels were not preserved: %+v", obs.Labels)
	}
	if obs.Duration != 25*time.Millisecond {
		t.Fatalf("duration = %s, want 25ms", obs.Duration)
	}
	if obs.OccurredAt != time.Unix(10, 0) {
		t.Fatalf("occurred_at = %s, want explicit time", obs.OccurredAt)
	}

	attrs := obs.Attributes()
	for _, forbidden := range []string{"top-secret-token", "sessionid=sensitive", "abc123", "vendor-secret", "another-secret"} {
		if strings.Contains(strings.Join(attributeValues(attrs), " "), forbidden) {
			t.Fatalf("attributes leaked secret %q: %#v", forbidden, attrs)
		}
	}
	for _, forbiddenKey := range []string{"prompt", "completion", "request", "output", "content", "call_id"} {
		if _, ok := attrs[forbiddenKey]; ok {
			t.Fatalf("attributes contain high-risk payload field %q: %#v", forbiddenKey, attrs)
		}
	}
	if attrs["status"] != string(StatusUnavailable) {
		t.Fatalf("status attribute = %q", attrs["status"])
	}
	if attrs["error_reason"] != string(ReasonError) {
		t.Fatalf("error reason = %q, want %q", attrs["error_reason"], ReasonError)
	}
}

func TestContextErrorsClassifyToStableStatusAndReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        error
		wantStatus Status
		wantReason Reason
	}{
		{name: "canceled", err: context.Canceled, wantStatus: StatusUnavailable, wantReason: ReasonCanceled},
		{name: "deadline", err: context.DeadlineExceeded, wantStatus: StatusUnavailable, wantReason: ReasonDeadlineExceeded},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			obs, err := NewObservation(Input{
				Labels: Labels{
					Backend:   "anthropic",
					Model:     "claude-3-5-haiku",
					Plane:     PlaneProviderBillable,
					Source:    SourceProviderCountAPI,
					Authority: AuthorityAuthoritative,
				},
				Err:      tt.err,
				Duration: time.Millisecond,
			})
			if err != nil {
				t.Fatalf("NewObservation returned error: %v", err)
			}
			if obs.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", obs.Status, tt.wantStatus)
			}
			if obs.UnavailableReason != tt.wantReason {
				t.Fatalf("unavailable reason = %q, want %q", obs.UnavailableReason, tt.wantReason)
			}
		})
	}
}

func TestStatsAggregateBoundedDimensions(t *testing.T) {
	t.Parallel()

	stats := NewStats()
	observations := []Input{
		{
			Labels: Labels{Backend: "openai", Model: "gpt-4.1-mini", Plane: PlaneProviderBillable, Source: SourceProviderCountAPI, Authority: AuthorityAuthoritative},
			Status: StatusSuccess, Duration: 10 * time.Millisecond,
		},
		{
			Labels: Labels{Backend: "openai", Model: "gpt-4.1-mini", Plane: PlaneProviderBillable, Source: SourceLocalTokenizer, Authority: AuthorityEstimated},
			Status: StatusUnavailable, FallbackReason: "provider_unavailable", UnavailableReason: "unsupported_model", Duration: 20 * time.Millisecond,
		},
	}
	for _, input := range observations {
		obs, err := NewObservation(input)
		if err != nil {
			t.Fatalf("NewObservation returned error: %v", err)
		}
		stats.Record(obs)
	}

	snapshot := stats.Snapshot()
	if got := snapshot.SourceSelections[SourceProviderCountAPI]; got != 1 {
		t.Fatalf("provider source count = %d, want 1", got)
	}
	if got := snapshot.SourceSelections[SourceLocalTokenizer]; got != 1 {
		t.Fatalf("local source count = %d, want 1", got)
	}
	if got := snapshot.FallbackReasons[ReasonProviderUnavailable]; got != 1 {
		t.Fatalf("fallback provider_unavailable count = %d, want 1", got)
	}
	if got := snapshot.UnavailableReasons[ReasonUnsupportedModel]; got != 1 {
		t.Fatalf("unavailable unsupported_model count = %d, want 1", got)
	}
	if snapshot.LatencyCount != 2 || snapshot.LatencySum != 30*time.Millisecond {
		t.Fatalf("latency count/sum = %d/%s, want 2/30ms", snapshot.LatencyCount, snapshot.LatencySum)
	}
	if len(snapshot.Latencies) != 2 || snapshot.Latencies[0] != 10*time.Millisecond || snapshot.Latencies[1] != 20*time.Millisecond {
		t.Fatalf("latencies = %#v, want [10ms 20ms]", snapshot.Latencies)
	}
}

func TestStatsConcurrentRecordSnapshotAndSetSink(t *testing.T) {
	t.Parallel()

	stats := NewStats()
	obs, err := NewObservation(Input{
		Labels: Labels{
			Backend:   "openai",
			Model:     "gpt-4.1-mini",
			Plane:     PlaneProviderBillable,
			Source:    SourceProviderCountAPI,
			Authority: AuthorityAuthoritative,
		},
		Status:   StatusSuccess,
		Duration: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewObservation returned error: %v", err)
	}

	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			for j := range 128 {
				stats.Record(obs)
				_ = stats.Snapshot()
				if j%16 == 0 {
					stats.SetSink(noopSink{})
				}
			}
		})
	}
	wg.Wait()

	snapshot := stats.Snapshot()
	if snapshot.LatencyCount != 32*128 {
		t.Fatalf("latency count = %d, want %d", snapshot.LatencyCount, 32*128)
	}
	if got := snapshot.SourceSelections[SourceProviderCountAPI]; got != 32*128 {
		t.Fatalf("source count = %d, want %d", got, 32*128)
	}
}

func TestValidationRejectsInvalidObservations(t *testing.T) {
	t.Parallel()

	validLabels := Labels{
		Backend:   "openai",
		Model:     "gpt-4.1-mini",
		Plane:     PlaneProviderBillable,
		Source:    SourceProviderCountAPI,
		Authority: AuthorityAuthoritative,
	}
	tests := []struct {
		name  string
		input Input
	}{
		{name: "missing backend", input: Input{Labels: Labels{Model: "m", Plane: PlaneProviderBillable, Source: SourceProviderCountAPI, Authority: AuthorityAuthoritative}}},
		{name: "unknown plane", input: Input{Labels: Labels{Backend: "b", Model: "m", Source: SourceProviderCountAPI, Authority: AuthorityAuthoritative}}},
		{name: "unknown source", input: Input{Labels: Labels{Backend: "b", Model: "m", Plane: PlaneProviderBillable, Authority: AuthorityAuthoritative}}},
		{name: "unknown authority", input: Input{Labels: Labels{Backend: "b", Model: "m", Plane: PlaneProviderBillable, Source: SourceProviderCountAPI}}},
		{name: "unknown status", input: Input{Labels: validLabels, Status: Status("weird")}},
		{name: "negative duration", input: Input{Labels: validLabels, Status: StatusSuccess, Duration: -time.Nanosecond}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := NewObservation(tt.input); err == nil {
				t.Fatal("NewObservation returned nil error")
			}
		})
	}
}

func attributeValues(attrs map[string]string) []string {
	values := make([]string, 0, len(attrs))
	for _, value := range attrs {
		values = append(values, value)
	}
	return values
}

type noopSink struct{}

func (noopSink) Record(Observation) {}
