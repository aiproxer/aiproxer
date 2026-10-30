package controlplane_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

func TestMemoryFallbackNeverReportsDistributedStrict(t *testing.T) {
	t.Parallel()
	got := controlplane.EnforcementScopeForStoreBacking("memory", true)
	if got != controlplane.EnforcementScopeAdvisorySingleProcess {
		t.Fatalf("scope=%q want advisory_single_process", got)
	}
	got = controlplane.EnforcementScopeForStoreBacking("MEMORY", true)
	if got != controlplane.EnforcementScopeAdvisorySingleProcess {
		t.Fatalf("scope=%q want advisory_single_process for uppercase memory", got)
	}
}

func TestPostgresStrictCapableReportsDistributedStrict(t *testing.T) {
	t.Parallel()
	got := controlplane.EnforcementScopeForStoreBacking("postgres", true)
	if got != controlplane.EnforcementScopeDistributedStrict {
		t.Fatalf("scope=%q want distributed_strict", got)
	}
}

func TestAggregateProtectedTrafficPostureFromIndependentComponents(t *testing.T) {
	t.Parallel()
	now := time.Unix(1700000000, 0).UTC()
	components := []controlplane.ReadinessComponentStatus{
		{
			Component:        controlplane.ReadinessComponentMeteringJournal,
			State:            controlplane.CapabilityReady,
			EnforcementScope: controlplane.EnforcementScopeAdvisorySingleProcess,
			LastUpdatedAt:    now,
		},
		{
			Component:        controlplane.ReadinessComponentUsageAuthority,
			State:            controlplane.CapabilityReady,
			EnforcementScope: controlplane.EnforcementScopeDistributedStrict,
			LastUpdatedAt:    now,
		},
		{
			Component:        controlplane.ReadinessComponentConcurrencyAuthority,
			State:            controlplane.CapabilityDegraded,
			EnforcementScope: controlplane.EnforcementScopeAdvisorySingleProcess,
			LastUpdatedAt:    now,
		},
	}
	posture := controlplane.AggregateProtectedTrafficPosture(components, now)
	if posture.State != controlplane.CapabilityDegraded {
		t.Fatalf("state=%q want degraded", posture.State)
	}
	if !posture.MayServeStrict {
		t.Fatal("strict distributed authority ready should allow strict traffic")
	}
}

func TestAggregateProtectedTrafficPostureUnavailableWhenStrictAuthorityDown(t *testing.T) {
	t.Parallel()
	now := time.Unix(1700000000, 0).UTC()
	components := []controlplane.ReadinessComponentStatus{
		{
			Component:        controlplane.ReadinessComponentUsageAuthority,
			State:            controlplane.CapabilityUnavailable,
			EnforcementScope: controlplane.EnforcementScopeDistributedStrict,
		},
	}
	posture := controlplane.AggregateProtectedTrafficPosture(components, now)
	if posture.State != controlplane.CapabilityUnavailable {
		t.Fatalf("state=%q want unavailable", posture.State)
	}
	if posture.MayServeStrict {
		t.Fatal("strict authority unavailable must not allow strict traffic")
	}
}

func TestAggregateProtectedTrafficPostureBillingSpoolIsAdvisory(t *testing.T) {
	t.Parallel()
	now := time.Unix(1700000000, 0).UTC()
	posture := controlplane.AggregateProtectedTrafficPosture([]controlplane.ReadinessComponentStatus{
		{
			Component:        controlplane.ReadinessComponentBillingSpool,
			State:            controlplane.CapabilityUnavailable,
			Reason:           controlplane.ReasonStoreNotReady,
			EnforcementScope: controlplane.EnforcementScopeAdvisorySingleProcess,
		},
	}, now)
	if posture.State != controlplane.CapabilityUnavailable {
		t.Fatalf("state=%q want unavailable", posture.State)
	}
	if !posture.MayServeStrict {
		t.Fatal("advisory billing spool must not block strict traffic posture")
	}
}

func TestReadinessComponentBillingSpoolIDIsStable(t *testing.T) {
	t.Parallel()
	if got := controlplane.ReadinessComponentBillingSpool; got != "billing_spool" {
		t.Fatalf("component=%q want billing_spool", got)
	}
}

func TestReadinessReportJSONRoundTripIndependentComponents(t *testing.T) {
	t.Parallel()
	now := time.Unix(1700000000, 0).UTC()
	report := controlplane.ReadinessReport{
		Components: []controlplane.ReadinessComponentStatus{
			{Component: controlplane.ReadinessComponentMeteringJournal, State: controlplane.CapabilityReady, EnforcementScope: controlplane.EnforcementScopeAdvisorySingleProcess, LastUpdatedAt: now},
			{Component: controlplane.ReadinessComponentControlPlane, State: controlplane.CapabilityReady, EnforcementScope: controlplane.EnforcementScopeAdvisorySingleProcess, LastUpdatedAt: now},
		},
		Posture: controlplane.ProtectedTrafficPosture{State: controlplane.CapabilityReady, MayServeStrict: true, LastUpdatedAt: now},
	}
	raw := roundTripJSON(t, report)
	for _, key := range []string{`"components"`, `"posture"`, `"metering_journal"`, `"may_serve_strict"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("expected %s in %s", key, raw)
		}
	}
}

func TestReadinessReportTypesAvoidForbiddenFields(t *testing.T) {
	t.Parallel()
	forbidden := []string{"Bearer", "APIKey", "Secret", "OAuth", "Header", "Password", "RawPayload", "DSN", "SQL"}
	assertNoForbiddenFields(t, controlplane.ReadinessReport{}, forbidden)
	assertNoForbiddenFields(t, controlplane.ReadinessComponentStatus{}, forbidden)
}
