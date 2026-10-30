package controlplane_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

func TestNormalizeObservedUsageExposesDualPlaneFields(t *testing.T) {
	t.Parallel()
	n := newTestNormalizer(t)
	ev := usage.Event{
		TraceID:      "trace-dp",
		BLegID:       "bleg-1",
		AttemptSeq:   2,
		BackendID:    "openai",
		InputTokens:  11,
		OutputTokens: 7,
		TotalTokens:  18,
		RecordedAt:   time.Now(),
	}
	got, err := n.FromUsage(ev)
	if err != nil {
		t.Fatalf("FromUsage: %v", err)
	}
	u := got.Usage()
	if u == nil {
		t.Fatal("usage detail missing")
	}
	if u.Plane != cp.UsagePlaneObserved || u.Availability != cp.UsageAvailabilityObserved {
		t.Fatalf("legacy projection lost: plane=%q availability=%q", u.Plane, u.Availability)
	}
	if u.Perspective != cp.UsagePerspectiveOperator {
		t.Fatalf("perspective = %q, want operator", u.Perspective)
	}
	if u.Boundary != cp.UsageBoundaryBackendEgress {
		t.Fatalf("boundary = %q, want backend_egress", u.Boundary)
	}
	if u.LifecycleScope != cp.UsageLifecycleBackendAttempt {
		t.Fatalf("lifecycle_scope = %q, want backend_attempt", u.LifecycleScope)
	}
	if u.Provenance != cp.UsageProvenanceDelegated {
		t.Fatalf("provenance = %q, want delegated", u.Provenance)
	}
	if string(u.Perspective) == string(u.Provenance) || string(u.Boundary) == string(u.Provenance) {
		t.Fatalf("perspective, boundary, and provenance must be independent: %#v", u)
	}
	if u.FactKind != cp.UsageFactKindDelta {
		t.Fatalf("fact_kind = %q, want delta", u.FactKind)
	}
	if err := controlplane.ValidateEvent(got); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestProjectUsageRowCopiesDualPlaneFields(t *testing.T) {
	t.Parallel()
	ev := cp.Event{
		Category: cp.CategoryUsage,
		Detail: &cp.UsageDetail{
			Plane:          cp.UsagePlaneObserved,
			Availability:   cp.UsageAvailabilityObserved,
			Perspective:    cp.UsagePerspectiveOperator,
			Boundary:       cp.UsageBoundaryBackendEgress,
			LifecycleScope: cp.UsageLifecycleBackendAttempt,
			Provenance:     cp.UsageProvenanceDelegated,
			FactKind:       cp.UsageFactKindDelta,
			TotalTokens:    9,
		},
		EvidenceState: cp.EvidenceRecorded,
	}
	row := controlplane.UsageRowFromEvent(ev)
	if row.Perspective != cp.UsagePerspectiveOperator || row.Boundary != cp.UsageBoundaryBackendEgress {
		t.Fatalf("usage row projection lost dual-plane fields: %#v", row)
	}
}

func TestProjectMeteringFact_PreservesAuthoritativeZeroPresence(t *testing.T) {
	t.Parallel()
	f := metering.Fact{
		FactID:      "be-egress:b-1:1",
		StreamID:    "be-ingress:b-1",
		Sequence:    1,
		Kind:        metering.FactKindCumulative,
		Perspective: metering.PerspectiveOperator,
		Boundary:    metering.BoundaryBackendEgress,
		Lifecycle:   metering.LifecycleBackendAttempt,
		Correlation: metering.Correlation{
			TraceID: "trace-1", RequestID: "req-1", ALegID: "a-1", BLegID: "b-1", AttemptID: "b-1",
		},
		BackendID: "backend-1",
		Model:     "model-1",
		Quantities: []metering.Quantity{
			{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 0, Present: true},
			{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 0, Present: true},
		},
		Money:     &metering.MoneyObservation{NanoUnits: 0, Currency: "USD", Present: true, Source: metering.SourceProviderReported},
		Source:    metering.SourceProviderReported,
		Authority: metering.AuthorityAuthoritative,
		Presence:  metering.PresencePresent,
	}
	u := controlplane.ProjectMeteringFact(f)
	if !u.TokenPresence.InputTokens || !u.TokenPresence.OutputTokens {
		t.Fatalf("token presence lost: %+v", u.TokenPresence)
	}
	if u.InputTokens != 0 || u.OutputTokens != 0 {
		t.Fatalf("authoritative zero tokens lost: in=%d out=%d", u.InputTokens, u.OutputTokens)
	}
	if !u.CostPresent || u.CostNanoUnits != 0 || u.Currency != "USD" {
		t.Fatalf("authoritative zero cost lost: present=%v nano=%d cur=%q", u.CostPresent, u.CostNanoUnits, u.Currency)
	}

	row := controlplane.UsageRowFromMeteringFact(f)
	if row.Correlation.TraceID != "trace-1" || row.Correlation.RequestID != "req-1" ||
		row.Correlation.ALegID != "a-1" || row.Correlation.BLegID != "b-1" ||
		row.Correlation.BackendID != "backend-1" || row.Correlation.Model != "model-1" {
		t.Fatalf("correlation projection incomplete: %+v", row.Correlation)
	}
	if !row.CostPresent || row.CostNanoUnits != 0 {
		t.Fatalf("row cost presence lost: present=%v nano=%d", row.CostPresent, row.CostNanoUnits)
	}

	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var decoded cp.UsageRow
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.CostPresent || decoded.CostNanoUnits != 0 {
		t.Fatalf("JSON roundtrip lost authoritative zero: present=%v nano=%d raw=%s", decoded.CostPresent, decoded.CostNanoUnits, raw)
	}
	if !decoded.TokenPresence.InputTokens || !decoded.TokenPresence.OutputTokens {
		t.Fatalf("JSON roundtrip lost token presence: %+v", decoded.TokenPresence)
	}
}

func TestProjectMeteringFact_AbsentCostStaysAbsent(t *testing.T) {
	t.Parallel()
	f := metering.Fact{
		FactID:      "fe-egress:req-1:1",
		StreamID:    "fe-ingress:req-1",
		Sequence:    1,
		Kind:        metering.FactKindCumulative,
		Perspective: metering.PerspectiveCustomer,
		Boundary:    metering.BoundaryFrontendEgress,
		Lifecycle:   metering.LifecycleLogicalRequest,
		Correlation: metering.Correlation{TraceID: "req-1", RequestID: "req-1", ALegID: "a-1"},
		FrontendID:  "openai-responses",
		Quantities: []metering.Quantity{
			{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 3, Present: true},
		},
		Money:     nil,
		Source:    metering.SourceObserved,
		Authority: metering.AuthorityAuthoritative,
		Presence:  metering.PresencePresent,
	}
	u := controlplane.ProjectMeteringFact(f)
	if u.CostPresent || u.CostNanoUnits != 0 || u.Currency != "" {
		t.Fatalf("absent cost must stay absent: %+v", u)
	}
	row := controlplane.UsageRowFromMeteringFact(f)
	if row.Correlation.FrontendID != "openai-responses" || row.Correlation.TraceID != "req-1" {
		t.Fatalf("FE correlation incomplete: %+v", row.Correlation)
	}
	if row.CostPresent {
		t.Fatal("customer FE projection must not invent CostPresent")
	}
}

func TestProjectMeteringFact_AuthorityAvailabilityTruthTable(t *testing.T) {
	t.Parallel()
	base := metering.Fact{
		FactID: "f1", StreamID: "s1", Sequence: 1,
		Kind: metering.FactKindCumulative, Perspective: metering.PerspectiveOperator,
		Boundary: metering.BoundaryBackendEgress, Lifecycle: metering.LifecycleBackendAttempt,
		Presence: metering.PresencePresent,
	}
	cases := []struct {
		name       string
		authority  metering.Authority
		source     metering.Source
		wantPlane  cp.UsagePlane
		wantAvail  cp.UsageAvailability
		wantProven cp.UsageProvenance
	}{
		{
			name:      "unavailable",
			authority: metering.AuthorityUnavailable, source: metering.SourceProviderReported,
			wantPlane: cp.UsagePlaneAccounting, wantAvail: cp.UsageAvailabilityUnavailable,
			wantProven: cp.UsageProvenanceUnavailable,
		},
		{
			name:      "unavailable_observed",
			authority: metering.AuthorityUnavailable, source: metering.SourceObserved,
			wantPlane: cp.UsagePlaneObserved, wantAvail: cp.UsageAvailabilityUnavailable,
			wantProven: cp.UsageProvenanceUnavailable,
		},
		{
			name:      "estimated",
			authority: metering.AuthorityEstimated, source: metering.SourceEstimated,
			wantPlane: cp.UsagePlaneObserved, wantAvail: cp.UsageAvailabilityObserved,
			wantProven: cp.UsageProvenanceEstimated,
		},
		{
			name:      "advisory",
			authority: metering.AuthorityAdvisory, source: metering.SourceObserved,
			wantPlane: cp.UsagePlaneObserved, wantAvail: cp.UsageAvailabilityObserved,
			wantProven: cp.UsageProvenanceAdvisory,
		},
		{
			name:      "delegated",
			authority: metering.AuthorityDelegated, source: metering.SourceObserved,
			wantPlane: cp.UsagePlaneObserved, wantAvail: cp.UsageAvailabilityObserved,
			wantProven: cp.UsageProvenanceDelegated,
		},
		{
			name:      "authoritative_accounting",
			authority: metering.AuthorityAuthoritative, source: metering.SourceProviderReported,
			wantPlane: cp.UsagePlaneAccounting, wantAvail: cp.UsageAvailabilityAccountingAuth,
			wantProven: cp.UsageProvenanceAuthoritative,
		},
		{
			name:      "authoritative_observed",
			authority: metering.AuthorityAuthoritative, source: metering.SourceObserved,
			wantPlane: cp.UsagePlaneObserved, wantAvail: cp.UsageAvailabilityObserved,
			wantProven: cp.UsageProvenanceAuthoritative,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := base
			f.Authority = tc.authority
			f.Source = tc.source
			u := controlplane.ProjectMeteringFact(f)
			if u.Plane != tc.wantPlane || u.Availability != tc.wantAvail || u.Provenance != tc.wantProven {
				t.Fatalf("got plane=%q avail=%q proven=%q want plane=%q avail=%q proven=%q",
					u.Plane, u.Availability, u.Provenance, tc.wantPlane, tc.wantAvail, tc.wantProven)
			}
		})
	}
}
