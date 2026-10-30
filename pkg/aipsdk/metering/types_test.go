package metering_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestEconomicPerspective_IsKnown(t *testing.T) {
	t.Parallel()
	cases := []struct {
		v    metering.EconomicPerspective
		want bool
	}{
		{metering.PerspectiveCustomer, true},
		{metering.PerspectiveOperator, true},
		{metering.PerspectiveNone, true},
		{"", false},
		{"shared", false},
		{metering.EconomicPerspective("CUSTOMER"), false},
	}
	for _, tc := range cases {
		if got := tc.v.IsKnown(); got != tc.want {
			t.Fatalf("IsKnown(%q)=%v want %v", tc.v, got, tc.want)
		}
	}
}

func TestBoundary_IsKnown(t *testing.T) {
	t.Parallel()
	known := []metering.Boundary{
		metering.BoundaryFrontendIngress,
		metering.BoundaryBackendIngress,
		metering.BoundaryBackendEgress,
		metering.BoundaryFrontendEgress,
	}
	for _, b := range known {
		if !b.IsKnown() {
			t.Fatalf("%q should be known", b)
		}
	}
	if metering.Boundary("").IsKnown() || metering.Boundary("derived:x").IsKnown() {
		t.Fatal("unknown boundaries must not report known")
	}
}

func TestLifecycleScope_IsKnown(t *testing.T) {
	t.Parallel()
	known := []metering.LifecycleScope{
		metering.LifecycleLogicalRequest,
		metering.LifecycleBackendAttempt,
		metering.LifecycleAuxiliaryRequest,
	}
	for _, s := range known {
		if !s.IsKnown() {
			t.Fatalf("%q should be known", s)
		}
	}
	if metering.LifecycleScope("").IsKnown() {
		t.Fatal("empty lifecycle must not be known")
	}
}

func TestFactKind_IsKnown(t *testing.T) {
	t.Parallel()
	known := []metering.FactKind{
		metering.FactKindDelta,
		metering.FactKindCumulative,
		metering.FactKindCorrection,
		metering.FactKindAuthoritativeReplacement,
		metering.FactKindReservationEstimate,
		metering.FactKindUnavailable,
	}
	for _, k := range known {
		if !k.IsKnown() {
			t.Fatalf("%q should be known", k)
		}
	}
	if metering.FactKind("").IsKnown() {
		t.Fatal("empty fact kind must not be known")
	}
}

func TestPresence_Source_Authority_Surfaced_AttemptOutcome_IsKnown(t *testing.T) {
	t.Parallel()
	if !metering.PresencePresent.IsKnown() || !metering.PresenceAbsent.IsKnown() || !metering.PresenceUnknown.IsKnown() {
		t.Fatal("presence enums")
	}
	if metering.Presence("").IsKnown() {
		t.Fatal("empty presence")
	}
	if !metering.SourceObserved.IsKnown() || !metering.SourceDerived.IsKnown() || !metering.SourceProviderReported.IsKnown() {
		t.Fatal("source enums")
	}
	if !metering.AuthorityAuthoritative.IsKnown() || !metering.AuthorityDelegated.IsKnown() ||
		!metering.AuthorityEstimated.IsKnown() || !metering.AuthorityAdvisory.IsKnown() ||
		!metering.AuthorityUnavailable.IsKnown() {
		t.Fatal("authority enums")
	}
	if !metering.SurfacedYes.IsKnown() || !metering.SurfacedNo.IsKnown() || !metering.SurfacedUnknown.IsKnown() {
		t.Fatal("surfaced enums")
	}
	if !metering.AttemptOutcomeWinner.IsKnown() || !metering.AttemptOutcomeLoser.IsKnown() ||
		!metering.AttemptOutcomeCanceled.IsKnown() || !metering.AttemptOutcomeFailed.IsKnown() ||
		!metering.AttemptOutcomeUnknown.IsKnown() {
		t.Fatal("attempt outcome enums")
	}
}

func TestValidateEnums(t *testing.T) {
	t.Parallel()
	if err := metering.PerspectiveCustomer.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := metering.EconomicPerspective("x").Validate(); err == nil {
		t.Fatal("expected validate error")
	}
	if err := metering.BoundaryFrontendIngress.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := metering.Boundary("x").Validate(); err == nil {
		t.Fatal("expected boundary validate error")
	}
}
