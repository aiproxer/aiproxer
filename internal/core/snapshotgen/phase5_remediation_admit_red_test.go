package snapshotgen_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/internal/core/snapshotgen"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/runtimegen"
)

func remediationAdmit(requestID string) authority.RequestAdmission {
	return authority.RequestAdmission{
		RequestID:   requestID,
		ALegID:      "a-1",
		Perspective: metering.PerspectiveCustomer,
		Lifecycle:   metering.LifecycleLogicalRequest,
		Exposure: economics.ExposureBasis{
			Perspective: metering.PerspectiveCustomer,
			Boundary:    metering.BoundaryFrontendIngress,
			Lifecycle:   metering.LifecycleLogicalRequest,
		},
	}
}

func TestPhase5Remediation_FiveToTwoAdmitUsesLiveCoordinators(t *testing.T) {
	t.Parallel()
	p := snapshotgen.NewPublisher()
	g1, err := snapshotgen.CompileExecutable(runtimegen.GenerationContribution{
		SourceID:                "static",
		Version:                 "g1-five",
		State:                   economics.SnapshotReady,
		ConcurrencyRegistration: concReg("conc", stubConcurrencyProvider{id: "conc"}),
		MaxActiveRequests:       5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if g1.RequestCoord == nil || g1.RequestCoord.Concurrency == nil {
		t.Fatal("g1 must own a live request coordinator concurrency object")
	}
	pub1, err := p.PublishExecutable(g1)
	if err != nil {
		t.Fatal(err)
	}
	pub1.Retain()
	held := p.LookupExecutable(pub1.ID)
	var leases []authoritycoord.CompositeDecision
	for i := range 5 {
		d, err := held.RequestCoord.Admit(context.Background(), remediationAdmit(fmt.Sprintf("old-%d", i)))
		if err != nil {
			t.Fatalf("g1 admit %d: %v", i, err)
		}
		if d.Kind != authority.DecisionAllow {
			t.Fatalf("g1 admit %d kind=%v", i, d.Kind)
		}
		leases = append(leases, d)
	}
	if _, err := held.RequestCoord.Admit(context.Background(), remediationAdmit("old-overflow")); err == nil {
		t.Fatal("g1 max=5 must deny the sixth admit")
	}

	g2, err := snapshotgen.CompileExecutable(runtimegen.GenerationContribution{
		SourceID:                "static",
		Version:                 "g2-two",
		State:                   economics.SnapshotReady,
		ConcurrencyRegistration: concReg("conc", stubConcurrencyProvider{id: "conc"}),
		MaxActiveRequests:       2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.PublishExecutable(g2); err != nil {
		t.Fatal(err)
	}
	cur := p.CurrentExecutable()
	if cur.RequestCoord == nil || cur.EnforcementMaxActive() != 2 {
		t.Fatalf("current must enforce max=2 via live coordinator, got %+v", cur)
	}
	for i := range 2 {
		if _, err := cur.RequestCoord.Admit(context.Background(), remediationAdmit(fmt.Sprintf("new-%d", i))); err != nil {
			t.Fatalf("g2 admit %d: %v", i, err)
		}
	}
	if _, err := cur.RequestCoord.Admit(context.Background(), remediationAdmit("new-overflow")); err == nil {
		t.Fatal("g2 max=2 must deny the third new admit")
	}
	if held.EnforcementMaxActive() != 5 {
		t.Fatalf("in-flight generation mutated: max=%d", held.EnforcementMaxActive())
	}
	if held.Version == cur.Version {
		t.Fatal("generation evidence must change with generation")
	}
	for _, d := range leases {
		_ = held.RequestCoord.Release(context.Background(), d.Stack)
	}
}
