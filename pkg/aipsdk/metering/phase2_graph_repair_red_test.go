package metering_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase2Repair_ComponentSchemaRelationshipCountIsBounded(t *testing.T) {
	t.Parallel()

	relationships := make([]metering.ComponentRelationship, metering.MaxComponentSchemaRelationships+1)
	for i := range relationships {
		relationships[i] = metering.ComponentRelationship{
			Kind: metering.RelationshipSubset,
			Parent: metering.ComponentKey{
				Direction: metering.DirectionInput,
				Component: fmt.Sprintf("provider_parent_%d", i),
				Unit:      metering.UnitToken,
				SchemaID:  "provider:bounded:v1",
			},
			Child: metering.ComponentKey{
				Direction: metering.DirectionInput,
				Component: fmt.Sprintf("provider_child_%d", i),
				Unit:      metering.UnitToken,
				SchemaID:  "provider:bounded:v1",
			},
		}
	}
	schema := metering.ComponentSchema{ID: "provider:bounded:v1", Version: "1", Relationships: relationships}
	if err := schema.Validate(); err == nil {
		t.Fatalf("schema with %d relationships must exceed the explicit bound", len(relationships))
	}
	withinBound := schema
	withinBound.Relationships = relationships[:metering.MaxComponentSchemaRelationships]
	if err := withinBound.Validate(); err != nil {
		t.Fatalf("schema at the explicit relationship bound must remain valid: %v", err)
	}
}

func TestPhase2Repair_CoverageGraphRejectsDuplicateRevisionNodes(t *testing.T) {
	t.Parallel()

	base := validV2Observation(t)
	for _, tc := range []struct {
		name string
		make func() metering.Observation
	}{
		{
			name: "identical duplicate",
			make: func() metering.Observation { return base.Clone() },
		},
		{
			name: "conflicting payload",
			make: func() metering.Observation {
				candidate := base.Clone()
				candidate.SourceEventKey = "event-conflict"
				return candidate
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := metering.ValidateCoverageGraph([]metering.Observation{base, tc.make()}); err == nil {
				t.Fatal("coverage graph must reject duplicate store/observation/revision nodes")
			}
		})
	}
}

func TestPhase2Repair_CoverageGraphPreservesLateUnresolvedReferences(t *testing.T) {
	t.Parallel()

	observation := validV2Observation(t)
	observation.Charges = []metering.ReportedCharge{{
		ChargeItemID: "aggregate",
		Amount:       decimalPtr("2"),
		Currency:     "USD",
		Kind:         metering.ChargeKindAggregate,
		Covers: []metering.ChargeCoverageRef{{
			Ref: metering.ChargeRef{
				StoreID:       "store-1",
				ObservationID: "late-child",
				Revision:      1,
				ChargeItemID:  "child",
			},
			Relation: metering.CoverageInclusive,
		}},
	}}
	if err := metering.ValidateCoverageGraph([]metering.Observation{observation}); err != nil {
		t.Fatalf("unresolved late coverage reference must remain pending: %v", err)
	}

	correction := validV2Observation(t)
	correction.ID = "correction"
	correction.SourceEventKey = "correction-event"
	correction.Semantics = metering.SemanticsCorrection
	correction.Supersedes = []metering.ObservationRef{{
		StoreID:       "store-1",
		ObservationID: "late-prior",
		Revision:      1,
		PayloadHash:   "exact-hash-pending-resolution",
	}}
	if err := metering.ValidateSupersessionGraph([]metering.Observation{correction}); err != nil {
		t.Fatalf("unresolved exact supersession reference must remain pending: %v", err)
	}
}

func TestPhase2Repair_SupersessionUnknownHashRequiresTrustedLegacyTuple(t *testing.T) {
	t.Parallel()

	prior := validV2Observation(t)
	prior.ID = "v2-prior"
	prior.SourceEventKey = "v2-prior-event"
	correction := validV2Observation(t)
	correction.ID = "v2-correction"
	correction.SourceEventKey = "v2-correction-event"
	correction.Semantics = metering.SemanticsCorrection
	correction.Supersedes = []metering.ObservationRef{{
		StoreID:       "store-1",
		ObservationID: prior.ID,
		Revision:      prior.Revision,
		PayloadHash:   metering.LegacyV1UnknownHash,
	}}
	if err := metering.ValidateSupersessionGraph([]metering.Observation{prior, correction}); err == nil {
		t.Fatal("generic V2 correction must not use the legacy unknown-hash escape")
	}

	priorWire, err := json.Marshal(prior)
	if err != nil {
		t.Fatal(err)
	}
	var genericPrior metering.Observation
	if err := json.Unmarshal(priorWire, &genericPrior); err != nil {
		t.Fatal(err)
	}
	if err := genericPrior.Validate(); err != nil {
		t.Fatalf("generic V2 prior should remain inspectable: %v", err)
	}
	if err := correction.Validate(); err == nil {
		t.Fatal("generic V2 correction must reject the unknown hash before graph resolution")
	}
}

func TestPhase2Repair_SupersessionRequiresExactHashForV2AndPreservesTrustedV1Hash(t *testing.T) {
	t.Parallel()

	prior := validV2Observation(t)
	prior.ID = "exact-prior"
	prior.SourceEventKey = "exact-prior-event"
	correction := validV2Observation(t)
	correction.ID = "exact-correction"
	correction.SourceEventKey = "exact-correction-event"
	correction.Semantics = metering.SemanticsCorrection
	correction.Supersedes = []metering.ObservationRef{{
		StoreID:       "store-1",
		ObservationID: prior.ID,
		Revision:      prior.Revision,
		PayloadHash:   prior.Fingerprint(),
	}}
	if err := metering.ValidateSupersessionGraph([]metering.Observation{prior, correction}); err != nil {
		t.Fatalf("exact V2 supersession hash must resolve: %v", err)
	}

	legacyPrior, legacyCorrection := trustedLegacyCorrectionPair(t)
	if err := metering.ValidateSupersessionGraph([]metering.Observation{legacyPrior, legacyCorrection}); err != nil {
		t.Fatalf("trusted V1 unknown-hash supersession must remain replayable: %v", err)
	}

	legacyPriorWire, err := json.Marshal(legacyPrior)
	if err != nil {
		t.Fatal(err)
	}
	var genericLegacyPrior metering.Observation
	if err := json.Unmarshal(legacyPriorWire, &genericLegacyPrior); err != nil {
		t.Fatal(err)
	}
	if err := metering.ValidateSupersessionGraph([]metering.Observation{genericLegacyPrior, legacyCorrection}); err == nil {
		t.Fatal("generic JSON decoding must not prove a legacy referenced prior record")
	}
}

func TestPhase2Repair_SupersessionRejectsMixedUnknownAndExactHashes(t *testing.T) {
	t.Parallel()

	prior := validV2Observation(t)
	correction := validV2Observation(t)
	correction.ID = "mixed-hash-correction"
	correction.SourceEventKey = "mixed-hash-event"
	correction.Semantics = metering.SemanticsCorrection
	correction.Supersedes = []metering.ObservationRef{
		{
			StoreID:       "store-1",
			ObservationID: prior.ID,
			Revision:      prior.Revision,
			PayloadHash:   metering.LegacyV1UnknownHash,
		},
		{
			StoreID:       "store-1",
			ObservationID: prior.ID,
			Revision:      prior.Revision,
			PayloadHash:   prior.Fingerprint(),
		},
	}
	if err := correction.Validate(); err == nil {
		t.Fatal("one V2 revision cannot be referenced with both unknown and exact hashes")
	}
}

func trustedLegacyCorrectionPair(t *testing.T) (metering.Observation, metering.Observation) {
	t.Helper()
	base := metering.Fact{
		StreamID: "legacy-stream", Perspective: metering.PerspectiveOperator,
		Boundary: metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Correlation: metering.Correlation{RequestID: "legacy-request", ALegID: "legacy-a", BLegID: "legacy-b", AttemptID: "legacy-attempt"},
		Source:      metering.SourceProviderReported, Authority: metering.AuthorityAuthoritative,
		Presence: metering.PresencePresent, Quantities: []metering.Quantity{{
			Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 2, Present: true,
		}},
	}
	priorFact := base
	priorFact.FactID = "legacy-prior"
	priorFact.Sequence = 1
	priorFact.Kind = metering.FactKindDelta
	correctionFact := base
	correctionFact.FactID = "legacy-correction"
	correctionFact.Sequence = 2
	correctionFact.Kind = metering.FactKindCorrection
	correctionFact.Supersedes = []string{priorFact.FactID}
	prior, err := metering.ObservationFromFact(priorFact)
	if err != nil {
		t.Fatal(err)
	}
	correction, err := metering.ObservationFromFact(correctionFact)
	if err != nil {
		t.Fatal(err)
	}
	return prior, correction
}
