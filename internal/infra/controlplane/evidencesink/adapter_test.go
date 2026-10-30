package evidencesink

import (
	"context"
	"errors"
	"testing"
	"time"

	corecontrolplane "github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

type failingAppender struct{}

func (failingAppender) Append(context.Context, cp.Event) (cp.RecordResult, error) {
	return cp.RecordResult{}, errors.New("control-plane append failed")
}

func requiredEvidenceEvent() cp.Event {
	now := time.Unix(100, 0).UTC()
	return cp.Event{
		SourceEventKey: "authority-required-evidence",
		Category:       cp.CategoryAccountingAuthority,
		OccurredAt:     now,
		RecordedAt:     now,
		Source:         cp.SourceRef{Name: "usageauthority", Version: "test"},
		Visibility:     cp.VisibilityDefault,
		EvidenceState:  cp.EvidenceRecorded,
		RedactionState: cp.RedactionNone,
		Detail: &cp.AccountingAuthorityDetail{
			Outcome:        cp.AccountingOutcomeAllow,
			ReasonCode:     "allowed",
			Authority:      cp.AccountingAuthoritySourceAuthoritative,
			EvidenceState:  cp.EvidenceRecorded,
			RedactionState: cp.RedactionNone,
		},
	}
}

func TestAdapterRequiredAccountingEvidencePropagatesRecorderFailure(t *testing.T) {
	t.Parallel()

	status := corecontrolplane.NewStatus(cp.CapabilityStatus{
		State:           cp.CapabilityReady,
		RecordingPolicy: cp.RecordingRequiredPreWork,
	})
	recorder := corecontrolplane.NewRecorderService(failingAppender{}, status, corecontrolplane.RecorderConfig{
		Policy:   cp.RecordingRequiredPreWork,
		Required: []cp.Category{cp.CategoryAccountingAuthority},
	})
	adapter := New(recorder, nil)
	err := adapter.RecordAccountingAuthority(context.Background(), requiredEvidenceEvent())
	if !errors.Is(err, usagelimit.ErrRequiredEvidence) {
		t.Fatalf("required recorder failure = %v, want ErrRequiredEvidence", err)
	}
}
