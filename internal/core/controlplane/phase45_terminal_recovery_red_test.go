package controlplane_test

import (
	"context"
	"testing"
	"time"

	corecp "github.com/aiproxer/aiproxer/internal/core/controlplane"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// Phase 4.5 RED/GREEN: readiness report includes independent terminal_recovery
// (requirements 12.1, 12.2, 12.4).

func TestPhase45_ReadinessReportIncludesTerminalRecovery(t *testing.T) {
	t.Parallel()
	now := time.Unix(1700000100, 0).UTC()
	svc := corecp.NewReadinessReportService(corecp.ReadinessReportSources{
		Now: func() time.Time { return now },
		TerminalRecovery: func(context.Context) (cp.ReadinessComponentStatus, error) {
			return cp.ReadinessComponentStatus{
				Component:        cp.ReadinessComponentTerminalRecovery,
				State:            cp.CapabilityDegraded,
				Reason:           cp.ReasonPendingTerminalWork,
				EnforcementScope: cp.EnforcementScopeAdvisorySingleProcess,
				StoreBacking:     "memory",
			}, nil
		},
	})
	report, err := svc.Report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var found *cp.ReadinessComponentStatus
	for i := range report.Components {
		if report.Components[i].Component == cp.ReadinessComponentTerminalRecovery {
			found = &report.Components[i]
			break
		}
	}
	if found == nil {
		t.Fatal("missing terminal_recovery component")
	}
	if found.State != cp.CapabilityDegraded || found.Reason != cp.ReasonPendingTerminalWork {
		t.Fatalf("terminal_recovery=%+v", found)
	}
	if report.Posture.State != cp.CapabilityDegraded {
		t.Fatalf("posture=%#v want degraded", report.Posture)
	}
}

func TestPhase45_TerminalRecoveryDisabledWhenSourceNil(t *testing.T) {
	t.Parallel()
	now := time.Unix(1700000101, 0).UTC()
	svc := corecp.NewReadinessReportService(corecp.ReadinessReportSources{
		Now: func() time.Time { return now },
	})
	report, err := svc.Report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range report.Components {
		if c.Component == cp.ReadinessComponentTerminalRecovery {
			if c.State != cp.CapabilityDisabled {
				t.Fatalf("nil source must report disabled, got %+v", c)
			}
			return
		}
	}
	t.Fatal("terminal_recovery row missing (must appear as disabled when unset)")
}
