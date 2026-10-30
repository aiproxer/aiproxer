package controlplane_test

import (
	"context"
	"testing"
	"time"

	corecp "github.com/aiproxer/aiproxer/internal/core/controlplane"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

func TestReadinessReportServiceReportsIndependentComponents(t *testing.T) {
	t.Parallel()
	now := time.Unix(1700000000, 0).UTC()
	svc := corecp.NewReadinessReportService(corecp.ReadinessReportSources{
		Now: func() time.Time { return now },
		ControlPlane: func(context.Context) (cp.CapabilityStatus, error) {
			return cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort}, nil
		},
		UsageAuthority: func(context.Context) (cp.AccountingAuthorityStatus, error) {
			return cp.AccountingAuthorityStatus{State: cp.AccountingAuthorityReady}, nil
		},
		ConcurrencyAuthority: func(context.Context) (cp.ConcurrencyAuthorityStatus, error) {
			return cp.ConcurrencyAuthorityStatus{State: cp.ConcurrencyAuthorityReady}, nil
		},
		MeteringJournal: func(context.Context) (cp.ReadinessComponentStatus, error) {
			return cp.ReadinessComponentStatus{
				Component:        cp.ReadinessComponentMeteringJournal,
				State:            cp.CapabilityReady,
				EnforcementScope: cp.EnforcementScopeAdvisorySingleProcess,
				StoreBacking:     "memory",
			}, nil
		},
		SnapshotStates: func() (usage, concurrency, rating cp.CapabilityState) {
			return cp.CapabilityReady, cp.CapabilityReady, cp.CapabilityReady
		},
		RequestCoordinatorEnabled: true,
		AttemptCoordinatorEnabled: true,
		StoreBackings: corecp.ReadinessStoreBackings{
			ControlPlane: "memory",
			Metering:     "memory",
			Usage:        "postgres",
			Concurrency:  "memory",
		},
	})
	report, err := svc.Report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Components) < 6 {
		t.Fatalf("components=%d want at least 6 independent rows", len(report.Components))
	}
	seen := map[cp.ReadinessComponentID]bool{}
	for _, c := range report.Components {
		seen[c.Component] = true
		if c.Component == cp.ReadinessComponentMeteringJournal && c.EnforcementScope != cp.EnforcementScopeAdvisorySingleProcess {
			t.Fatalf("memory journal scope=%q", c.EnforcementScope)
		}
		if c.Component == cp.ReadinessComponentUsageAuthority && c.EnforcementScope != cp.EnforcementScopeDistributedStrict {
			t.Fatalf("postgres usage scope=%q", c.EnforcementScope)
		}
	}
	for _, id := range []cp.ReadinessComponentID{
		cp.ReadinessComponentMeteringJournal,
		cp.ReadinessComponentControlPlane,
		cp.ReadinessComponentUsageAuthority,
		cp.ReadinessComponentConcurrencyAuthority,
		cp.ReadinessComponentRequestCoordinator,
		cp.ReadinessComponentAttemptCoordinator,
	} {
		if !seen[id] {
			t.Fatalf("missing component %q", id)
		}
	}
	if report.Posture.State != cp.CapabilityReady {
		t.Fatalf("posture=%#v", report.Posture)
	}
}
