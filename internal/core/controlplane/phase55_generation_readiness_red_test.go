package controlplane_test

import (
	"context"
	"testing"
	"time"

	corecp "github.com/aiproxer/aiproxer/internal/core/controlplane"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

func TestPhase55_ReportSeparatesExecutableFromSourceFetchAndTerminal(t *testing.T) {
	t.Parallel()
	now := time.Unix(1700000100, 0).UTC()
	svc := corecp.NewReadinessReportService(corecp.ReadinessReportSources{
		Now: func() time.Time { return now },
		SnapshotStates: func() (usage, concurrency, rating cp.CapabilityState) {
			return cp.CapabilityDegraded, cp.CapabilityDegraded, cp.CapabilityReady
		},
		ExecutableGeneration: func() cp.ExecutableGenerationStatus {
			return cp.ExecutableGenerationStatus{
				ID:               42,
				Version:          "exec-v9",
				State:            cp.CapabilityReady,
				EvidenceObjectID: "eval-obj-9",
				SourceID:         "static-config",
			}
		},
		TerminalRecovery: func(context.Context) (cp.ReadinessComponentStatus, error) {
			return cp.ReadinessComponentStatus{
				Component:   cp.ReadinessComponentTerminalRecovery,
				State:       cp.CapabilityDegraded,
				Reason:      cp.ReasonPendingTerminalWork,
				ProviderIDs: []string{"missing-effect"},
			}, nil
		},
	})
	report, err := svc.Report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.ExecutableGeneration.ID != 42 || report.ExecutableGeneration.Version != "exec-v9" {
		t.Fatalf("executable status=%#v", report.ExecutableGeneration)
	}
	if report.ExecutableGeneration.EvidenceObjectID != "eval-obj-9" {
		t.Fatalf("evidence=%q", report.ExecutableGeneration.EvidenceObjectID)
	}
	if report.ExecutableGeneration.State != cp.CapabilityReady {
		t.Fatalf("executable state=%q want ready despite degraded source fetch", report.ExecutableGeneration.State)
	}
	byID := map[cp.ReadinessComponentID]cp.ReadinessComponentStatus{}
	for _, c := range report.Components {
		byID[c.Component] = c
	}
	execRow, ok := byID[cp.ReadinessComponentExecutableGeneration]
	if !ok {
		t.Fatal("missing executable_generation component")
	}
	if execRow.GenerationID != 42 || execRow.GenerationVersion != "exec-v9" || execRow.EvidenceObjectID != "eval-obj-9" {
		t.Fatalf("exec row=%#v", execRow)
	}
	if byID[cp.ReadinessComponentUsageSnapshot].State != cp.CapabilityDegraded {
		t.Fatalf("usage source fetch want degraded, got %q", byID[cp.ReadinessComponentUsageSnapshot].State)
	}
	term := byID[cp.ReadinessComponentTerminalRecovery]
	if term.State != cp.CapabilityDegraded || len(term.ProviderIDs) != 1 || term.ProviderIDs[0] != "missing-effect" {
		t.Fatalf("terminal row=%#v", term)
	}
	if execRow.EvidenceObjectID == term.ProviderIDs[0] {
		t.Fatal("executable evidence must not equal terminal unresolved provider id")
	}
}
