package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
)

// boundVersionAssertService fails settle unless BoundVersion matches the
// admission-time pin — proving lifecycle does not silently settle on "latest".
type boundVersionAssertService struct {
	recordingAuthorityService
	wantVersion string
}

func (s *boundVersionAssertService) Settle(ctx context.Context, in usagelimit.SettleInput) (usagelimit.SettleResult, error) {
	if in.BoundVersion.Version != s.wantVersion {
		return usagelimit.SettleResult{}, errAuthorityLifecycleSettleFailed
	}
	return s.recordingAuthorityService.Settle(ctx, in)
}

func TestAuthorityLifecycle_SettleRequiresBoundVersionAfterPublish(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Simulate admit under v1, then a mid-flight publish to v2 on the rule plane.
	// Lifecycle must still settle with the bound v1 ref (requirement 11.4).
	svc := &boundVersionAssertService{wantVersion: "v1"}
	state := attemptAuthorityState{
		admissionInput: testAuthorityAdmissionInput(100),
		admissionResult: usagelimit.AdmissionResult{
			Reserved:       true,
			ReservationID:  "r-bound",
			ReservedAmount: authorityInputAmount(100),
			BoundVersion: economics.PolicySnapshotRef{
				VersionRef: economics.VersionRef{ID: "usage_authority", Version: "v1"},
				PolicyID:   "usage_authority",
			},
		},
	}
	l := newAuthorityLifecycle(svc, nil, state, routing.AttemptCandidate{})
	if applied := l.Settle(ctx, usagelimit.SettlementKindFinal, aipapi.Event{}, false); !applied {
		t.Fatal("settle must succeed when BoundVersion is forwarded from admission")
	}
	if got := svc.lastSettle().BoundVersion.Version; got != "v1" {
		t.Fatalf("bound version = %q, want v1", got)
	}

	// Empty BoundVersion must not be treated as success after a fictional v2 publish.
	svc2 := &boundVersionAssertService{wantVersion: "v1"}
	stateMissing := state
	stateMissing.admissionResult.BoundVersion = economics.PolicySnapshotRef{}
	l2 := newAuthorityLifecycle(svc2, nil, stateMissing, routing.AttemptCandidate{})
	if applied := l2.Settle(ctx, usagelimit.SettlementKindFinal, aipapi.Event{}, false); applied {
		t.Fatal("settle must fail when BoundVersion is omitted after publish (would use latest)")
	}
}
