package controlplane_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

func TestPhase55_ExecutableGenerationComponentAndStatusArePublic(t *testing.T) {
	t.Parallel()
	if controlplane.ReadinessComponentExecutableGeneration != "executable_generation" {
		t.Fatalf("component=%q", controlplane.ReadinessComponentExecutableGeneration)
	}
	now := time.Unix(1700000000, 0).UTC()
	status := controlplane.ExecutableGenerationStatus{
		ID:               7,
		Version:          "gen-v2",
		State:            controlplane.CapabilityReady,
		EvidenceObjectID: "op-rater-obj",
		SourceID:         "static-config",
		LastUpdatedAt:    now,
	}
	row := controlplane.ReadinessComponentStatus{
		Component:         controlplane.ReadinessComponentExecutableGeneration,
		State:             status.State,
		GenerationID:      status.ID,
		GenerationVersion: status.Version,
		EvidenceObjectID:  status.EvidenceObjectID,
		LastUpdatedAt:     now,
	}
	report := controlplane.ReadinessReport{
		Components:           []controlplane.ReadinessComponentStatus{row},
		ExecutableGeneration: status,
		Posture:              controlplane.ProtectedTrafficPosture{State: controlplane.CapabilityReady, MayServeStrict: true, LastUpdatedAt: now},
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		`"executable_generation"`, `"generation_id"`, `"generation_version"`,
		`"evidence_object_id"`, `"gen-v2"`, `"op-rater-obj"`,
	} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("missing %s in %s", key, raw)
		}
	}
	assertNoForbiddenFields(t, controlplane.ExecutableGenerationStatus{}, []string{
		"Coordinator", "authoritycoord", "RequestCoordinator", "AttemptCoordinator",
	})
}

func TestPhase55_AggregateTreatsExecutableGenerationIndependently(t *testing.T) {
	t.Parallel()
	now := time.Unix(1700000001, 0).UTC()
	components := []controlplane.ReadinessComponentStatus{
		{
			Component:        controlplane.ReadinessComponentExecutableGeneration,
			State:            controlplane.CapabilityReady,
			GenerationID:     1,
			EvidenceObjectID: "rater-a",
			LastUpdatedAt:    now,
		},
		{
			Component:     controlplane.ReadinessComponentUsageSnapshot,
			State:         controlplane.CapabilityDegraded,
			LastUpdatedAt: now,
		},
		{
			Component:     controlplane.ReadinessComponentTerminalRecovery,
			State:         controlplane.CapabilityDegraded,
			Reason:        controlplane.ReasonPendingTerminalWork,
			ProviderIDs:   []string{"ghost-provider"},
			LastUpdatedAt: now,
		},
	}
	posture := controlplane.AggregateProtectedTrafficPosture(components, now)
	if posture.State != controlplane.CapabilityDegraded {
		t.Fatalf("posture=%q want degraded from source/terminal, not executable", posture.State)
	}
	var exec controlplane.ReadinessComponentStatus
	for _, c := range components {
		if c.Component == controlplane.ReadinessComponentExecutableGeneration {
			exec = c
		}
	}
	if exec.State != controlplane.CapabilityReady || exec.EvidenceObjectID != "rater-a" {
		t.Fatalf("executable row mutated or mixed with terminal: %#v", exec)
	}
	if exec.EvidenceObjectID == "ghost-provider" {
		t.Fatal("evidence must name evaluator object, not unresolved terminal provider")
	}
}
