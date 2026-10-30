package runtime

import (
	"context"
	"sync/atomic"
	"testing"

	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// previewUAService counts Admit calls through the recorder so the
// nil-coordinator fallback test can assert the adapter path actually ran.
type previewUAService struct {
	previewAdmitRecorder
	calls atomic.Int32
}

func (s *previewUAService) Admit(ctx context.Context, in usagelimit.AdmissionInput) (usagelimit.AdmissionResult, error) {
	s.calls.Add(1)
	return s.previewAdmitRecorder.Admit(ctx, in)
}

// TestPreviewAndApplyAttemptClamps_NilCoordinatorUsesUsageAuthority proves the
// fallback: single-provider deployments without a multi-provider coordinator
// still get bounded, side-effect-free non-monetary clamp preview through the
// direct UsageAuthority adapter.
func TestPreviewAndApplyAttemptClamps_NilCoordinatorUsesUsageAuthority(t *testing.T) {
	t.Parallel()

	svc := &previewUAService{}
	ex := &Executor{AccountingRuntime: AccountingRuntime{UsageAuthority: svc}}
	// AttemptCoordinator intentionally nil.
	limit := 100
	call := aipapi.Call{ID: "req-ua", Options: aipapi.GenerationOptions{MaxOutputTokens: &limit}}
	_, ran, err := ex.previewAndApplyAttemptClamps(context.Background(), &call, authorityCandidate(), "a-1", "b-ua")
	if err != nil {
		t.Fatalf("previewAndApplyAttemptClamps: %v", err)
	}
	if !ran {
		t.Fatal("expected preview to run via UsageAuthority fallback")
	}
	if svc.calls.Load() < 1 {
		t.Fatal("expected UsageAuthority Admit via adapter PreviewAttempt")
	}
	if !svc.last.EstimateOnly || !svc.last.SkipEvidence {
		t.Fatalf("preview must be EstimateOnly+SkipEvidence; got EstimateOnly=%v SkipEvidence=%v",
			svc.last.EstimateOnly, svc.last.SkipEvidence)
	}
}
