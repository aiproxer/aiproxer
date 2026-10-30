package domain

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestReconcileProviderOnlySelectedForProviderBilling(t *testing.T) {
	t.Parallel()

	provider := usage(10, 20, aipapi.UsagePlaneProviderBillable, aipapi.UsageSourceProviderReported, aipapi.UsageAuthorityAuthoritative)

	got := Reconcile(PolicyBillProvider, provider)

	if !got.Complete {
		t.Fatalf("complete = false, reason %q", got.IncompleteReason)
	}
	if got.BillablePlane != aipapi.UsagePlaneProviderBillable {
		t.Fatalf("billable plane = %q", got.BillablePlane)
	}
	assertUsage(t, got.BillableUsage, 10, 20, 30)
	plane, ok := got.UsageForPlane(aipapi.UsagePlaneProviderBillable)
	if !ok {
		t.Fatal("provider plane missing")
	}
	assertUsage(t, plane, 10, 20, 30)
}

func TestReconcileTransformedResponsePreservesProviderAndClientVisibleUsage(t *testing.T) {
	t.Parallel()

	provider := usage(100, 50, aipapi.UsagePlaneProviderBillable, aipapi.UsageSourceProviderReported, aipapi.UsageAuthorityAuthoritative)
	client := usage(80, 40, aipapi.UsagePlaneClientVisible, aipapi.UsageSourceProxyAdjusted, aipapi.UsageAuthorityDelegated)

	tests := []struct {
		name      string
		policy    BillingPolicy
		wantPlane aipapi.UsagePlane
		wantIn    int
		wantOut   int
	}{
		{name: "provider billing", policy: PolicyBillProvider, wantPlane: aipapi.UsagePlaneProviderBillable, wantIn: 100, wantOut: 50},
		{name: "client visible billing", policy: PolicyBillClientVisible, wantPlane: aipapi.UsagePlaneClientVisible, wantIn: 80, wantOut: 40},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := Reconcile(tt.policy, provider, client)

			if !got.Complete {
				t.Fatalf("complete = false, reason %q", got.IncompleteReason)
			}
			if got.BillablePlane != tt.wantPlane {
				t.Fatalf("billable plane = %q, want %q", got.BillablePlane, tt.wantPlane)
			}
			assertUsage(t, got.BillableUsage, tt.wantIn, tt.wantOut, tt.wantIn+tt.wantOut)
			assertHasConflict(t, got.Conflicts, ConflictTransformedUsage)
			if _, ok := got.UsageForPlane(aipapi.UsagePlaneProviderBillable); !ok {
				t.Fatal("provider plane missing")
			}
			if _, ok := got.UsageForPlane(aipapi.UsagePlaneClientVisible); !ok {
				t.Fatal("client-visible plane missing")
			}
		})
	}
}

func TestReconcileLocalEstimateDoesNotOverwriteProviderAuthoritativeUsage(t *testing.T) {
	t.Parallel()

	provider := usage(100, 50, aipapi.UsagePlaneProviderBillable, aipapi.UsageSourceProviderReported, aipapi.UsageAuthorityAuthoritative)
	estimate := usage(95, 45, aipapi.UsagePlaneProviderBillable, aipapi.UsageSourceLocalEstimator, aipapi.UsageAuthorityEstimated)

	got := Reconcile(PolicyBillProvider, estimate, provider)

	if !got.Complete {
		t.Fatalf("complete = false, reason %q", got.IncompleteReason)
	}
	assertUsage(t, got.BillableUsage, 100, 50, 150)
	assertHasConflict(t, got.Conflicts, ConflictPlaneCandidates)
}

func TestReconcileStrictProviderRequiredMissingUsageIncomplete(t *testing.T) {
	t.Parallel()

	client := usage(20, 10, aipapi.UsagePlaneClientVisible, aipapi.UsageSourceProxyAdjusted, aipapi.UsageAuthorityDelegated)

	got := Reconcile(PolicyStrictProviderRequired, client)

	if got.Complete {
		t.Fatal("complete = true")
	}
	if !strings.Contains(got.IncompleteReason, string(aipapi.UsagePlaneProviderBillable)) {
		t.Fatalf("incomplete reason = %q", got.IncompleteReason)
	}
	if got.BillablePlane != aipapi.UsagePlaneUnknown {
		t.Fatalf("billable plane = %q", got.BillablePlane)
	}
}

func TestReconcileStrictProviderRequiredRejectsLocalProviderEstimate(t *testing.T) {
	t.Parallel()

	estimate := usage(20, 10, aipapi.UsagePlaneProviderBillable, aipapi.UsageSourceLocalEstimator, aipapi.UsageAuthorityEstimated)

	got := Reconcile(PolicyStrictProviderRequired, estimate)

	if got.Complete {
		t.Fatal("complete = true")
	}
	if !strings.Contains(got.IncompleteReason, "authoritative provider-reported") {
		t.Fatalf("incomplete reason = %q", got.IncompleteReason)
	}
	if got.BillablePlane != aipapi.UsagePlaneUnknown {
		t.Fatalf("billable plane = %q", got.BillablePlane)
	}
}

func TestReconcileStrictProviderRequiredAcceptsAuthoritativeProviderSources(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source aipapi.UsageSource
	}{
		{name: "provider reported", source: aipapi.UsageSourceProviderReported},
		{name: "provider count api", source: aipapi.UsageSourceProviderCountAPI},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider := usage(20, 10, aipapi.UsagePlaneProviderBillable, tt.source, aipapi.UsageAuthorityAuthoritative)

			got := Reconcile(PolicyStrictProviderRequired, provider)

			if !got.Complete {
				t.Fatalf("complete = false, reason %q", got.IncompleteReason)
			}
			assertUsage(t, got.BillableUsage, 20, 10, 30)
		})
	}
}

func TestReconcileStrictProviderRequiredRejectsDelegatedProviderReportedUsage(t *testing.T) {
	t.Parallel()

	delegated := usage(20, 10, aipapi.UsagePlaneProviderBillable, aipapi.UsageSourceProviderReported, aipapi.UsageAuthorityDelegated)

	got := Reconcile(PolicyStrictProviderRequired, delegated)

	if got.Complete {
		t.Fatal("complete = true")
	}
	if !strings.Contains(got.IncompleteReason, "authoritative provider-reported") {
		t.Fatalf("incomplete reason = %q", got.IncompleteReason)
	}
}

func TestReconcileEventsDeduplicatesCandidatesAndKeepsPolicyReservedAdditive(t *testing.T) {
	t.Parallel()

	provider := usage(12, 8, aipapi.UsagePlaneProviderBillable, aipapi.UsageSourceProviderReported, aipapi.UsageAuthorityAuthoritative)
	reservedA := usage(3, 2, aipapi.UsagePlaneProxyBillable, aipapi.UsageSourcePolicyReserved, aipapi.UsageAuthorityAuthoritative)
	reservedB := usage(4, 1, aipapi.UsagePlaneProxyBillable, aipapi.UsageSourcePolicyReserved, aipapi.UsageAuthorityAuthoritative)
	events := []aipapi.Event{
		{Kind: aipapi.EventUsageDelta, UsageScopes: []aipapi.ScopedUsageDelta{provider, reservedA}},
		{Kind: aipapi.EventUsageDelta, UsageScopes: []aipapi.ScopedUsageDelta{provider, reservedB}},
	}

	providerResult := ReconcileEvents(PolicyBillProvider, events...)
	assertUsage(t, providerResult.BillableUsage, 12, 8, 20)

	proxyResult := ReconcileEvents(PolicyBillProxyBillable, events...)
	if !proxyResult.Complete {
		t.Fatalf("complete = false, reason %q", proxyResult.IncompleteReason)
	}
	assertUsage(t, proxyResult.BillableUsage, 7, 3, 10)
}

func TestReconcileEventsPresenceDistinguishesEqualCounterCandidates(t *testing.T) {
	t.Parallel()
	first := usage(5, 0, aipapi.UsagePlaneProviderBillable, aipapi.UsageSourceProviderReported, aipapi.UsageAuthorityAuthoritative)
	first.UsagePresence.InputTokens = true
	second := first
	second.UsagePresence.OutputTokens = true

	result := ReconcileEvents(
		PolicyBillProvider,
		aipapi.Event{Kind: aipapi.EventUsageDelta, UsageScopes: []aipapi.ScopedUsageDelta{first}},
		aipapi.Event{Kind: aipapi.EventUsageDelta, UsageScopes: []aipapi.ScopedUsageDelta{second}},
	)
	if len(result.Conflicts) == 0 || result.Conflicts[0].Code != ConflictPlaneCandidates {
		t.Fatalf("conflicts = %+v, want distinct candidates", result.Conflicts)
	}
}

func TestReconcilePolicyReservedDoesNotHideLaterActualUsageForSamePlane(t *testing.T) {
	t.Parallel()

	reserved := usage(3, 2, aipapi.UsagePlaneProxyBillable, aipapi.UsageSourcePolicyReserved, aipapi.UsageAuthorityAuthoritative)
	actual := usage(12, 8, aipapi.UsagePlaneProxyBillable, aipapi.UsageSourceProxyAdjusted, aipapi.UsageAuthorityDelegated)

	got := Reconcile(PolicyBillProxyBillable, reserved, actual)

	if !got.Complete {
		t.Fatalf("complete = false, reason %q", got.IncompleteReason)
	}
	assertUsage(t, got.BillableUsage, 12, 8, 20)
	assertHasConflict(t, got.Conflicts, ConflictPlaneCandidates)
}

func TestReconcilePolicyReservedDoesNotInflateProviderActualUsageForSamePlane(t *testing.T) {
	t.Parallel()

	reserved := usage(5, 5, aipapi.UsagePlaneProviderBillable, aipapi.UsageSourcePolicyReserved, aipapi.UsageAuthorityAuthoritative)
	provider := usage(100, 50, aipapi.UsagePlaneProviderBillable, aipapi.UsageSourceProviderReported, aipapi.UsageAuthorityAuthoritative)

	got := Reconcile(PolicyBillProvider, reserved, provider)

	if !got.Complete {
		t.Fatalf("complete = false, reason %q", got.IncompleteReason)
	}
	assertUsage(t, got.BillableUsage, 100, 50, 150)
	assertHasConflict(t, got.Conflicts, ConflictPlaneCandidates)
}

func TestReconcileUnknownPlaneAndSourceAreExplicitWarnings(t *testing.T) {
	t.Parallel()

	unknown := usage(0, 0, aipapi.UsagePlaneUnknown, aipapi.UsageSourceUnknown, aipapi.UsageAuthorityUnknown)

	got := Reconcile(PolicyBillProvider, unknown)

	if got.Complete {
		t.Fatal("complete = true")
	}
	assertHasWarning(t, got.Warnings, WarningUnknownPlane)
	assertHasWarning(t, got.Warnings, WarningUnknownSource)
	assertHasWarning(t, got.Warnings, WarningZeroUsage)
}

func usage(input, output int, plane aipapi.UsagePlane, source aipapi.UsageSource, authority aipapi.UsageAuthority) aipapi.ScopedUsageDelta {
	return aipapi.ScopedUsageDelta{
		InputTokens:  input,
		OutputTokens: output,
		TotalTokens:  input + output,
		Accounting: aipapi.UsageAccountingMetadata{
			Plane:     plane,
			Source:    source,
			Authority: authority,
		},
	}
}

func assertUsage(t *testing.T, got aipapi.ScopedUsageDelta, input, output, total int) {
	t.Helper()
	if got.InputTokens != input || got.OutputTokens != output || got.TotalTokens != total {
		t.Fatalf("usage = input %d output %d total %d, want input %d output %d total %d", got.InputTokens, got.OutputTokens, got.TotalTokens, input, output, total)
	}
}

func assertHasConflict(t *testing.T, conflicts []Conflict, code ConflictCode) {
	t.Helper()
	for _, conflict := range conflicts {
		if conflict.Code == code {
			return
		}
	}
	t.Fatalf("conflict %q missing from %#v", code, conflicts)
}

func assertHasWarning(t *testing.T, warnings []Warning, code WarningCode) {
	t.Helper()
	for _, warning := range warnings {
		if warning.Code == code {
			return
		}
	}
	t.Fatalf("warning %q missing from %#v", code, warnings)
}
