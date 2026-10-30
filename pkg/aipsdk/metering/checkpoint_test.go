package metering_test

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestCheckpoint_Validate(t *testing.T) {
	t.Parallel()
	ok := metering.Checkpoint{
		CheckpointID: "cp-1",
		StreamID:     "stream-1",
		Boundary:     metering.BoundaryFrontendIngress,
		Lifecycle:    metering.LifecycleLogicalRequest,
		Perspective:  metering.PerspectiveCustomer,
		Correlation:  metering.Correlation{RequestID: "req-1", ALegID: "a-1"},
		CapturedAt:   time.Unix(1, 0).UTC(),
		Presence:     metering.PresenceUnknown,
	}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	missingID := ok
	missingID.CheckpointID = ""
	if err := missingID.Validate(); err == nil {
		t.Fatal("checkpoint_id required")
	}
	badBoundary := ok
	badBoundary.Boundary = metering.BoundaryBackendEgress
	if err := badBoundary.Validate(); err == nil {
		t.Fatal("frontend ingress checkpoint must use frontend_ingress boundary")
	}
}

func TestCheckpoint_BindScopeDoesNotRequireCall(t *testing.T) {
	t.Parallel()
	cp := metering.Checkpoint{
		CheckpointID: "cp-1",
		StreamID:     "s-1",
		Boundary:     metering.BoundaryFrontendIngress,
		Lifecycle:    metering.LifecycleLogicalRequest,
		Perspective:  metering.PerspectiveCustomer,
		Correlation:  metering.Correlation{RequestID: "r"},
		Presence:     metering.PresenceAbsent,
		CapturedAt:   time.Unix(1, 0).UTC(),
	}
	cp.Scope = scope.PrincipalScopeView{PrincipalID: scope.Known("p1")}
	if err := cp.Validate(); err != nil {
		t.Fatal(err)
	}
}
