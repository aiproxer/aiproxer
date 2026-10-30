package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// previewAdmitRecorder captures Admit inputs from usageAuthorityProviderAdapter.
type previewAdmitRecorder struct {
	last usagelimit.AdmissionInput
}

func (r *previewAdmitRecorder) Admit(_ context.Context, in usagelimit.AdmissionInput) (usagelimit.AdmissionResult, error) {
	r.last = in
	return usagelimit.AdmissionResult{Allowed: true}, nil
}

func (r *previewAdmitRecorder) Settle(context.Context, usagelimit.SettleInput) (usagelimit.SettleResult, error) {
	return usagelimit.SettleResult{}, nil
}

func (r *previewAdmitRecorder) Release(context.Context, usagelimit.ReleaseInput) (usagelimit.ReleaseResult, error) {
	return usagelimit.ReleaseResult{}, nil
}

func (r *previewAdmitRecorder) ApplyUsage(context.Context, usagelimit.ApplyUsageCommand) (usagelimit.ApplyUsageResult, error) {
	return usagelimit.ApplyUsageResult{}, nil
}

func TestPreviewAttemptSkipsDurableEvidence(t *testing.T) {
	t.Parallel()

	rec := &previewAdmitRecorder{}
	adapter := newUsageAuthorityProviderAdapter(rec)
	_, err := adapter.PreviewAttempt(context.Background(), authority.AttemptAdmission{
		RequestID: "req-1",
		AttemptID: "b-1",
		BLegID:    "b-1",
		ALegID:    "a-1",
		BackendID: "backend-1",
		Model:     "model-1",
		Exposure: economics.ExposureBasis{
			Perspective: metering.PerspectiveOperator,
			Boundary:    metering.BoundaryBackendIngress,
			Lifecycle:   metering.LifecycleBackendAttempt,
			Money: economics.Money{
				NanoUnits: 100,
				Currency:  "USD",
				Present:   true,
			},
		},
	})
	if err != nil {
		t.Fatalf("PreviewAttempt: %v", err)
	}
	if !rec.last.EstimateOnly {
		t.Fatal("PreviewAttempt must use EstimateOnly")
	}
	if !rec.last.SkipEvidence {
		t.Fatal("PreviewAttempt must set SkipEvidence so Admit does not record durable evidence")
	}
}

func TestAdmitAttemptRecordsEvidence(t *testing.T) {
	t.Parallel()

	rec := &previewAdmitRecorder{}
	adapter := newUsageAuthorityProviderAdapter(rec)
	_, err := adapter.AdmitAttempt(context.Background(), authority.AttemptAdmission{
		RequestID: "req-1",
		AttemptID: "b-1",
		BLegID:    "b-1",
		ALegID:    "a-1",
	})
	if err != nil {
		t.Fatalf("AdmitAttempt: %v", err)
	}
	if rec.last.EstimateOnly {
		t.Fatal("AdmitAttempt must not be estimate-only")
	}
	if rec.last.SkipEvidence {
		t.Fatal("AdmitAttempt must record durable evidence")
	}
}

// Retired money clamp mapping has no implementation.
func TestUsageAuthorityProviderAdapter_PreservesAdmissionDecisionOnError(t *testing.T) {
	t.Parallel()
	cause := errors.New("authority reader unavailable")
	bound := economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "usage-authority", Version: "v1"}, PolicyID: "usage-authority"}
	svc := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{BoundVersion: bound},
		admitErr:    cause,
	}
	adapter := newUsageAuthorityProviderAdapter(svc)

	decision, err := adapter.AdmitRequest(context.Background(), authority.RequestAdmission{RequestID: "req-1", ALegID: "a-1"})
	if !errors.Is(err, cause) {
		t.Fatalf("error=%v want cause", err)
	}
	if decision.Kind != authority.DecisionDeny {
		t.Fatalf("kind=%s want fail-closed deny from accompanying admission result", decision.Kind)
	}
	if len(decision.BoundVersions) != 1 || decision.BoundVersions[0] != bound {
		t.Fatalf("bound versions=%+v want %+v", decision.BoundVersions, bound)
	}
}

type appliedEvidenceErrService struct {
	previewAdmitRecorder
}

func (s *appliedEvidenceErrService) Settle(context.Context, usagelimit.SettleInput) (usagelimit.SettleResult, error) {
	return usagelimit.SettleResult{Applied: true, ReservationID: "h1"}, errors.New("evidence write failed")
}

func TestSettleAttemptAppliedWithEvidenceErrorIsSuccess(t *testing.T) {
	t.Parallel()

	adapter := newUsageAuthorityProviderAdapter(&appliedEvidenceErrService{})
	got, err := adapter.SettleAttempt(context.Background(), authority.AttemptSettlement{
		RequestID: "req-1",
		AttemptID: "att-1",
		Handles:   []string{"h1"},
	})
	if err != nil {
		t.Fatalf("applied settlement must not fail the coordinator stage: %v", err)
	}
	if got.Kind != authority.SettlementFinal {
		t.Fatalf("kind=%v want %v", got.Kind, authority.SettlementFinal)
	}
	if got.Handle != "h1" {
		t.Fatalf("handle=%q want h1", got.Handle)
	}
}
