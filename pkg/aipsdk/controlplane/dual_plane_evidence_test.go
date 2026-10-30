package controlplane_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

func TestUsageDetailExposesIndependentPerspectiveBoundaryProvenance(t *testing.T) {
	t.Parallel()
	detail := controlplane.UsageDetail{
		Plane:          controlplane.UsagePlaneObserved,
		Availability:   controlplane.UsageAvailabilityObserved,
		Perspective:    controlplane.UsagePerspectiveOperator,
		Boundary:       controlplane.UsageBoundaryBackendEgress,
		LifecycleScope: controlplane.UsageLifecycleBackendAttempt,
		Provenance:     controlplane.UsageProvenanceDelegated,
		FactKind:       controlplane.UsageFactKindDelta,
		Surfaced:       controlplane.UsageSurfacedUnknown,
		PolicyVersion:  controlplane.VersionRef{ID: "usage-policy", Version: "v3"},
		InputTokens:    10,
		OutputTokens:   5,
		TotalTokens:    15,
	}
	if string(detail.Perspective) == string(detail.Provenance) || string(detail.Perspective) == string(detail.Boundary) {
		t.Fatalf("perspective must differ from provenance and boundary: %#v", detail)
	}
	if string(detail.Boundary) == string(detail.Provenance) {
		t.Fatalf("boundary must differ from provenance: %#v", detail)
	}
	if detail.Plane != controlplane.UsagePlaneObserved || detail.Availability != controlplane.UsageAvailabilityObserved {
		t.Fatalf("legacy plane/availability must remain: %#v", detail)
	}
}

func TestUsageDetailDualPlaneJSONRoundTrip(t *testing.T) {
	t.Parallel()
	now := time.Unix(1700000000, 0).UTC()
	detail := controlplane.UsageDetail{
		Plane:          controlplane.UsagePlaneObserved,
		Availability:   controlplane.UsageAvailabilityObserved,
		Perspective:    controlplane.UsagePerspectiveCustomer,
		Boundary:       controlplane.UsageBoundaryFrontendEgress,
		LifecycleScope: controlplane.UsageLifecycleLogicalRequest,
		Provenance:     controlplane.UsageProvenanceAuthoritative,
		FactKind:       controlplane.UsageFactKindCumulative,
		Surfaced:       controlplane.UsageSurfacedYes,
		PolicyVersion:  controlplane.VersionRef{ID: "policy-1", Version: "2026.07", EffectiveAt: now},
		RatingVersion:  controlplane.VersionRef{ID: "rater-1", Version: "rate-9"},
		TotalTokens:    42,
	}
	raw := roundTripJSON(t, detail)
	for _, key := range []string{"perspective", "boundary", "lifecycle_scope", "provenance", "fact_kind", "surfaced", "policy_version", "rating_version", "plane", "availability"} {
		if !strings.Contains(string(raw), `"`+key+`"`) {
			t.Fatalf("expected JSON key %q in %s", key, raw)
		}
	}
	var back controlplane.UsageDetail
	unmarshalJSON(t, raw, &back)
	if back.Perspective != detail.Perspective || back.Boundary != detail.Boundary || back.Provenance != detail.Provenance {
		t.Fatalf("dual-plane fields lost: %#v", back)
	}
	if !back.PolicyVersion.EffectiveAt.Equal(now) {
		t.Fatalf("policy version effective_at lost: %#v", back.PolicyVersion)
	}
}

func TestUsageRowCarriesDualPlaneFields(t *testing.T) {
	t.Parallel()
	row := controlplane.UsageRow{
		Correlation:    controlplane.Correlation{TraceID: "trace-1", BLegID: "b-1"},
		Plane:          controlplane.UsagePlaneAccounting,
		Availability:   controlplane.UsageAvailabilityAccountingAuth,
		Perspective:    controlplane.UsagePerspectiveOperator,
		Boundary:       controlplane.UsageBoundaryBackendIngress,
		LifecycleScope: controlplane.UsageLifecycleBackendAttempt,
		Provenance:     controlplane.UsageProvenanceEstimated,
		FactKind:       controlplane.UsageFactKindDelta,
		Surfaced:       controlplane.UsageSurfacedNo,
		EvidenceState:  controlplane.EvidenceRecorded,
		RedactionState: controlplane.RedactionNone,
	}
	raw := roundTripJSON(t, row)
	if !strings.Contains(string(raw), `"backend_ingress"`) {
		t.Fatalf("boundary must round-trip: %s", raw)
	}
}

func TestAccountingAuthorityDetailDualPlaneFieldsRoundTrip(t *testing.T) {
	t.Parallel()
	detail := controlplane.AccountingAuthorityDetail{
		Correlation:        controlplane.Correlation{TraceID: "t", RequestID: "req-parent", BLegID: "b"},
		Outcome:            controlplane.AccountingOutcomeReserve,
		AuthorityNamespace: "customer-ns",
		Perspective:        controlplane.UsagePerspectiveCustomer,
		LifecycleScope:     controlplane.UsageLifecycleLogicalRequest,
		Basis:              "frontend_ingress",
		RuleVersion:        "rule-v2",
		Surfaced:           controlplane.UsageSurfacedUnknown,
		ReservationType:    controlplane.AuthorityHandleReservation,
		ParentRequestID:    "req-parent",
		BoundRatingVersion: controlplane.VersionRef{ID: "rater", Version: "v1"},
		EvidenceState:      controlplane.EvidenceRecorded,
		RedactionState:     controlplane.RedactionNone,
	}
	raw := roundTripJSON(t, detail)
	for _, key := range []string{"authority_namespace", "perspective", "lifecycle_scope", "basis", "rule_version", "surfaced", "reservation_type", "parent_request_id", "bound_rating_version"} {
		if !strings.Contains(string(raw), `"`+key+`"`) {
			t.Fatalf("expected key %q in %s", key, raw)
		}
	}
	var back controlplane.AccountingAuthorityDetail
	unmarshalJSON(t, raw, &back)
	if back.AuthorityNamespace != "customer-ns" || back.RuleVersion != "rule-v2" || back.ParentRequestID != "req-parent" {
		t.Fatalf("authority dual-plane fields lost: %#v", back)
	}
}

func TestAccountingDecisionRowDualPlaneParityWithLimitStatus(t *testing.T) {
	t.Parallel()
	limit := controlplane.AccountingLimitStatusRow{
		AuthorityNamespace: "operator-ns",
		Perspective:        string(controlplane.UsagePerspectiveOperator),
		LifecycleScope:     string(controlplane.UsageLifecycleBackendAttempt),
		Basis:              "backend_egress",
		RuleVersion:        "v7",
	}
	decision := controlplane.AccountingDecisionRow{
		AuthorityNamespace: limit.AuthorityNamespace,
		Perspective:        controlplane.UsagePerspective(limit.Perspective),
		LifecycleScope:     controlplane.UsageLifecycleScope(limit.LifecycleScope),
		Basis:              limit.Basis,
		RuleVersion:        limit.RuleVersion,
		Surfaced:           controlplane.UsageSurfacedYes,
		ReservationType:    controlplane.AuthorityHandleReservation,
		BoundRatingVersion: controlplane.VersionRef{ID: "rate", Version: "r1"},
		Outcome:            controlplane.AccountingOutcomeReconcile,
		EvidenceState:      controlplane.EvidenceRecorded,
		RedactionState:     controlplane.RedactionSummarized,
	}
	raw, err := json.Marshal(decision)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"operator-ns"`) || !strings.Contains(string(raw), `"backend_egress"`) {
		t.Fatalf("decision row must carry dual-plane identity: %s", raw)
	}
}
