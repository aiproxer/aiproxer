package checkpoint_test

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Phase 1.1 RED: backend TraceID must not reuse FEStreamID (req 5.6, G-04).

func TestCaptureBackendIngress_TraceIDMustNotReuseFEStreamID(t *testing.T) {
	t.Parallel()

	const feStream = "fe-ingress:req-corr"
	snap, err := checkpoint.CaptureBackendIngress(checkpoint.BackendIngressInput{
		Call: aipapi.Call{
			ID:      "req-corr",
			Session: aipapi.SessionRef{ALegID: "a-1"},
			Messages: []aipapi.Message{{
				Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")},
			}},
		},
		AttemptID:    "att-1",
		BLegID:       "b-1",
		ALegID:       "a-1",
		BackendID:    "backend-1",
		Model:        "model-1",
		CheckpointID: "be-in",
		StreamID:     "be-ingress:att-1",
		Now:          time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	corr := snap.Public.Correlation
	if corr.RequestID != "req-corr" || corr.ALegID != "a-1" || corr.BLegID != "b-1" || corr.AttemptID != "att-1" {
		t.Fatalf("identity correlation incomplete: %+v", corr)
	}
	if corr.TraceID == feStream || corr.TraceID == snap.Public.StreamID {
		t.Fatalf("backend TraceID must not reuse FE stream id %q (misleading correlation)", corr.TraceID)
	}
}
