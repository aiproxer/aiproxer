package aipapi_test

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestAttemptRecord_roundTripFields(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Minute)
	rec := aipapi.AttemptRecord{
		BLegID:         "b1",
		ALegID:         "a1",
		Seq:            2,
		BackendID:      "openairesponses",
		EffectiveModel: "gpt-4",
		StartedAt:      start,
		FinishedAt:     end,
		Outcome:        aipapi.AttemptSwallowedFailure,
		Reason:         "upstream_timeout_pre_output",
	}
	if rec.Outcome != aipapi.AttemptSwallowedFailure {
		t.Fatalf("outcome: got %q", rec.Outcome)
	}
	if rec.Seq != 2 {
		t.Fatalf("seq: got %d", rec.Seq)
	}
}
