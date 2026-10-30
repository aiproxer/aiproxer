package metering_test

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

//go:embed testdata/refinement_phase1_multimodal_vectors.json
var phase2MultimodalVectors []byte

func TestPhase2V2_DecimalNormalizesAndConvertsCheckedNanos(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw       string
		canonical string
		nanos     int64
	}{
		{raw: "001.2300", canonical: "123/2", nanos: 1_230_000_000},
		{raw: "1.25e-1", canonical: "125/3", nanos: 125_000_000},
		{raw: "-0.000", canonical: "0/0", nanos: 0},
		{raw: "0.000000001", canonical: "1/9", nanos: 1},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			d, err := metering.ParseDecimal(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			if got := d.CanonicalString(); got != tc.canonical {
				t.Fatalf("canonical=%q want %q", got, tc.canonical)
			}
			got, err := d.ToNanoUnits()
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.nanos {
				t.Fatalf("nanos=%d want %d", got, tc.nanos)
			}
		})
	}
	if got := (metering.Decimal{Coefficient: "1000", Scale: 2}).CanonicalString(); got != "10/0" {
		t.Fatalf("fractional scale normalization changed value: got %q want %q", got, "10/0")
	}
	for _, raw := range []string{"1e-19", "1e39", "NaN", "Infinity", " 1", "1 "} {
		if _, err := metering.ParseDecimal(raw); err == nil {
			t.Fatalf("ParseDecimal(%q) must reject out-of-contract value", raw)
		}
	}
	fine, err := metering.ParseDecimal("1.0000000001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fine.ToNanoUnits(); err == nil {
		t.Fatal("sub-nano precision must be rejected at the checked ledger boundary")
	}
}

func TestPhase2V2_DecimalBoundsAndPresence(t *testing.T) {
	t.Parallel()
	if err := (metering.Decimal{Coefficient: "0001", Scale: 2}).Validate(); err == nil {
		t.Fatal("noncanonical coefficient must be rejected by Validate")
	}
	if err := (metering.Decimal{Coefficient: "1", Scale: 19}).Validate(); err == nil {
		t.Fatal("scale above 18 must be rejected")
	}
	if _, err := (metering.Decimal{Coefficient: "1", Scale: 10}).ToNanoUnits(); err == nil {
		t.Fatal("fractional ledger nano must not be rounded silently")
	}
	if _, err := (metering.Decimal{Coefficient: strings.Repeat("9", 39)}).Normalize(); err == nil {
		t.Fatal("coefficient above 38 digits must be rejected")
	}
}

func TestPhase2V2_ComponentKeyDirectionQualifiersAndCanonicalBytes(t *testing.T) {
	t.Parallel()
	input := metering.ComponentKey{
		Direction: metering.DirectionInput,
		Component: "image",
		Unit:      metering.UnitImage,
		SchemaID:  "provider:image:v1",
		Dimensions: []metering.Dimension{
			{Name: "quality", Value: "high"},
			{Name: "width", Value: "1024"},
		},
	}
	output := input
	output.Direction = metering.DirectionOutput
	if input.Equal(output) || input.CanonicalKey() == output.CanonicalKey() || input.Fingerprint() == output.Fingerprint() {
		t.Fatal("input and output keys must remain distinct")
	}
	reordered := input
	reordered.Dimensions = []metering.Dimension{{Name: "width", Value: "1024"}, {Name: "quality", Value: "high"}}
	if !input.Equal(reordered) || input.CanonicalKey() != reordered.CanonicalKey() {
		t.Fatal("dimension order must not affect canonical identity")
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	reorderedJSON, err := json.Marshal(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(inputJSON, reorderedJSON) {
		t.Fatal("direct ComponentKey JSON must use canonical dimension order")
	}
	if err := (metering.ComponentKey{Direction: metering.DirectionInput, Component: "image", Unit: metering.UnitImage, Dimensions: []metering.Dimension{{Name: "quality", Value: "high"}, {Name: "quality", Value: "low"}}}).Validate(); err == nil {
		t.Fatal("duplicate dimension names must be rejected")
	}
	unknown := metering.ComponentKey{Direction: metering.DirectionNone, Component: "vendor:credits", Unit: "credit", SchemaID: "vendor:credits:v1"}
	if err := unknown.Validate(); err != nil {
		t.Fatalf("schema-qualified unknown component should be retainable: %v", err)
	}
	if err := (metering.ComponentKey{Direction: metering.FlowDirection("resource"), Component: "storage", Unit: metering.UnitByteSecond, SchemaID: "resource:v1"}).Validate(); err == nil {
		t.Fatal("resource must remain a subject, not a flow direction")
	}
	if err := (metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentStorage, Unit: metering.UnitByteSecond}).Validate(); err == nil {
		t.Fatal("resource storage must use direction none")
	}
}

func TestPhase2V2_MultimodalNativeUnitsRoundTripFromFixture(t *testing.T) {
	t.Parallel()
	var vectors []struct {
		Media      string            `json:"media"`
		Direction  string            `json:"direction"`
		Unit       string            `json:"unit"`
		Qualifiers map[string]string `json:"qualifiers"`
	}
	if err := json.Unmarshal(phase2MultimodalVectors, &vectors); err != nil {
		t.Fatal(err)
	}
	for i, vector := range vectors {
		direction := metering.DirectionNone
		switch vector.Direction {
		case "input":
			direction = metering.DirectionInput
		case "output":
			direction = metering.DirectionOutput
		case "none":
		default:
			t.Fatalf("vector %d has unknown direction %q", i, vector.Direction)
		}
		dimensions := make([]metering.Dimension, 0, len(vector.Qualifiers))
		for name, value := range vector.Qualifiers {
			dimensions = append(dimensions, metering.Dimension{Name: name, Value: value})
		}
		key := metering.ComponentKey{Direction: direction, Component: vector.Media, Unit: vector.Unit, SchemaID: "fixture:multimodal:v1", Dimensions: dimensions}
		if err := key.Validate(); err != nil {
			t.Fatalf("vector %d (%s/%s/%s): %v", i, vector.Media, vector.Direction, vector.Unit, err)
		}
		wire, err := json.Marshal(key)
		if err != nil {
			t.Fatal(err)
		}
		var roundTrip metering.ComponentKey
		if err := json.Unmarshal(wire, &roundTrip); err != nil {
			t.Fatal(err)
		}
		if !roundTrip.Equal(key) {
			t.Fatalf("vector %d key changed across JSON round-trip: %#v != %#v", i, roundTrip, key)
		}
	}
}

func TestPhase2V2_ComponentSchemaRequiresExplicitRelationshipSemantics(t *testing.T) {
	t.Parallel()
	parent := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentInputToken, Unit: metering.UnitToken, SchemaID: "token:v1"}
	child := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentCacheReadInputToken, Unit: metering.UnitToken, SchemaID: "token:v1"}
	schema := metering.ComponentSchema{ID: "token:v1", Version: "1", Relationships: []metering.ComponentRelationship{{Kind: metering.RelationshipSubset, Parent: parent, Child: child}}}
	if err := schema.Validate(); err != nil {
		t.Fatal(err)
	}
	transformChild := metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentAudio, Unit: metering.UnitSecond, SchemaID: "token:v1"}
	transform := metering.ComponentSchema{ID: "token:v1", Version: "1", Relationships: []metering.ComponentRelationship{{Kind: metering.RelationshipTransform, Parent: parent, Child: transformChild}}}
	if err := transform.Validate(); err != nil {
		t.Fatal(err)
	}
	invalid := metering.ComponentSchema{ID: "token:v1", Version: "1", Relationships: []metering.ComponentRelationship{{Kind: metering.RelationshipAggregate, Parent: parent, Child: transformChild}}}
	if err := invalid.Validate(); err == nil {
		t.Fatal("non-transform relationships must not silently convert units")
	}
}

func TestPhase2V2_ObservationPreservesPresenceProvenanceAndDeepClone(t *testing.T) {
	t.Parallel()
	n := metering.Decimal{Coefficient: "125", Scale: 1}
	obs := validV2Observation(t)
	obs.Measures = []metering.Measure{
		{Key: mediaKey(metering.DirectionInput, metering.UnitSecond), Value: &n, Quality: metering.QualityObserved, MethodRef: "adapter:duration"},
		{Key: mediaKey(metering.DirectionOutput, metering.UnitSecond), Value: nil, Quality: metering.QualityUnavailable, Reason: "provider omitted"},
	}
	obs.Evidence = []metering.SafeEvidenceField{{Path: "$.usage.audio_seconds", Lexeme: "12.5", Present: true, Acquisition: "provider_response"}}
	if err := obs.Validate(); err != nil {
		t.Fatal(err)
	}
	clone := obs.Clone()
	clone.Measures[0].Key.Dimensions[0].Value = "changed"
	clone.Measures[0].Value.Coefficient = "999"
	clone.Evidence[0].Lexeme = "0"
	if obs.Measures[0].Key.Dimensions[0].Value == "changed" || obs.Measures[0].Value.Coefficient == "999" || obs.Evidence[0].Lexeme == "0" {
		t.Fatal("Clone must deep-copy nested slices and decimal pointers")
	}
	first, err := obs.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	clone.Measures[0].Key.Dimensions = append(clone.Measures[0].Key.Dimensions, metering.Dimension{Name: "extra", Value: "x"})
	second, err := obs.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("canonical serialization must be deterministic")
	}
	wire, err := json.Marshal(obs)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(wire, []byte("prompt")) || bytes.Contains(wire, []byte("secret")) {
		t.Fatal("observation wire shape must not expose raw sensitive payload fields")
	}
}

func TestPhase2V2_ObservationRejectsContradictoryProvenance(t *testing.T) {
	t.Parallel()
	obs := validV2Observation(t)
	obs.Origin = metering.OriginLocal
	if err := obs.Validate(); err == nil {
		t.Fatal("provider-response acquisition must not be labelled local origin")
	}
	obs = validV2Observation(t)
	obs.Origin = metering.OriginProvider
	obs.Acquisition = metering.AcquisitionStatementImporter
	if err := obs.Validate(); err == nil {
		t.Fatal("statement-import acquisition must not be labelled provider origin")
	}
	obs = validV2Observation(t)
	obs.Authority = metering.AuthorityVerifiedStatement
	if err := obs.Validate(); err == nil {
		t.Fatal("verified statement authority must retain statement origin")
	}
}

func TestPhase2V2_SubjectUnionAndCorrelationRejectForeignCombinations(t *testing.T) {
	t.Parallel()
	base := validV2Observation(t)
	base.Subject = metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "store-1", ALegID: "a-1", BillingCallID: "call-1", BLegID: "b-1"}
	base.Correlation.BLegID = "b-1"
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	foreign := base
	foreign.Subject.Kind = metering.SubjectAccountWindow
	foreign.Subject.BLegID = "b-1"
	foreign.Subject.ProviderAccountKey = "acct-1"
	foreign.Subject.PoolID = "primary"
	foreign.Subject.WindowID = "minute"
	if err := foreign.Validate(); err == nil {
		t.Fatal("account-window subject must reject B-leg lineage fields")
	}
	foreign = base
	foreign.Correlation.BLegID = "other-b-leg"
	if err := foreign.Validate(); err == nil {
		t.Fatal("subject/correlation B-leg mismatch must be rejected")
	}
}

func TestPhase2V2_ChargeCoverageStoreScopeAndCycles(t *testing.T) {
	t.Parallel()
	base := validV2Observation(t)
	base.Charges = []metering.ReportedCharge{
		{
			ChargeItemID: "aggregate", Amount: decimalPtr("12"), Currency: "USD", Kind: metering.ChargeKindAggregate,
			Covers: []metering.ChargeCoverageRef{{Ref: metering.ChargeRef{StoreID: "store-1", ObservationID: "obs-1", Revision: 1, ChargeItemID: "input"}, Relation: metering.CoverageInclusive}},
		},
	}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	badStore := base
	badStore.Charges[0].Covers[0].Ref.StoreID = "other-store"
	if err := badStore.Validate(); err == nil {
		t.Fatal("cross-store charge coverage must be rejected")
	}
	cycleA := base
	cycleB := base
	cycleA.ID, cycleB.ID = "obs-a", "obs-b"
	cycleA.SourceEventKey, cycleB.SourceEventKey = "event-a", "event-b"
	cycleA.Charges[0].Covers[0].Ref.ObservationID = "obs-b"
	cycleA.Charges[0].Covers[0].Ref.ChargeItemID = "child"
	cycleB.Charges = []metering.ReportedCharge{{
		ChargeItemID: "child", Amount: decimalPtr("1"), Currency: "USD", Kind: metering.ChargeKindComponent,
		Covers: []metering.ChargeCoverageRef{{Ref: metering.ChargeRef{StoreID: "store-1", ObservationID: "obs-a", Revision: 1, ChargeItemID: "aggregate"}, Relation: metering.CoverageInclusive}},
	}}
	if err := metering.ValidateCoverageGraph([]metering.Observation{cycleA, cycleB}); err == nil {
		t.Fatal("coverage cycle must be rejected")
	}
	contradictory := base
	contradictory.Charges[0].Covers = append(contradictory.Charges[0].Covers, metering.ChargeCoverageRef{Ref: metering.ChargeRef{StoreID: "store-1", ObservationID: "obs-1", Revision: 1, ChargeItemID: "input"}, Relation: metering.CoverageAdditive})
	if err := contradictory.Validate(); err == nil {
		t.Fatal("inclusive/additive duplicate edge must be rejected")
	}
}

func TestPhase2V2_SupersessionGraphRejectsCyclesAndCrossSubjectCorrections(t *testing.T) {
	t.Parallel()
	a := validV2Observation(t)
	a.ID, a.SourceEventKey, a.Semantics = "revision-a", "event-a", metering.SemanticsCorrection
	a.Supersedes = []metering.ObservationRef{{StoreID: "store-1", ObservationID: "revision-b", Revision: 1, PayloadHash: metering.LegacyV1UnknownHash}}
	b := validV2Observation(t)
	b.ID, b.SourceEventKey, b.Semantics = "revision-b", "event-b", metering.SemanticsCorrection
	b.Supersedes = []metering.ObservationRef{{StoreID: "store-1", ObservationID: "revision-a", Revision: 1, PayloadHash: metering.LegacyV1UnknownHash}}
	if err := metering.ValidateSupersessionGraph([]metering.Observation{a, b}); err == nil {
		t.Fatal("supersession cycle must be rejected")
	}
	prior := validV2Observation(t)
	prior.ID, prior.SourceEventKey = "prior", "prior-event"
	current := validV2Observation(t)
	current.ID, current.SourceEventKey, current.Semantics = "correction", "correction-event", metering.SemanticsCorrection
	current.Subject.BLegID, current.Correlation.BLegID = "other-b-leg", "other-b-leg"
	current.Supersedes = []metering.ObservationRef{{StoreID: "store-1", ObservationID: prior.ID, Revision: prior.Revision, PayloadHash: prior.Fingerprint()}}
	if err := metering.ValidateSupersessionGraph([]metering.Observation{prior, current}); err == nil {
		t.Fatal("correction across B-leg subjects must be rejected")
	}
}

func TestPhase2V2_ObservationRejectsDuplicateRevisionAndEvidenceReferences(t *testing.T) {
	t.Parallel()
	obs := validV2Observation(t)
	obs.Semantics = metering.SemanticsCorrection
	obs.Supersedes = []metering.ObservationRef{
		{StoreID: "store-1", ObservationID: "prior", Revision: 1, PayloadHash: "hash-a"},
		{StoreID: "store-1", ObservationID: "prior", Revision: 1, PayloadHash: "hash-b"},
	}
	if err := obs.Validate(); err == nil {
		t.Fatal("contradictory supersession payload hashes must be rejected")
	}
	obs = validV2Observation(t)
	obs.Evidence = []metering.SafeEvidenceField{
		{Path: "$.usage.audio_seconds", Lexeme: "1", Present: true, Acquisition: metering.AcquisitionProviderResponse},
		{Name: "$.usage.audio_seconds", Value: "1", Present: true, Acquisition: metering.AcquisitionProviderResponse},
	}
	if err := obs.Validate(); err == nil {
		t.Fatal("duplicate safe evidence identity must be rejected")
	}
}

func TestPhase2V2_V1ProjectionPreservesLegacyIdentityAndRejectsNonRepresentable(t *testing.T) {
	t.Parallel()
	f := metering.Fact{
		FactID: "fact-1", StreamID: "stream-1", Sequence: 3, Kind: metering.FactKindCumulative,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
		Lifecycle:   metering.LifecycleBackendAttempt,
		Correlation: metering.Correlation{RequestID: "req-1", ALegID: "a-1", BLegID: "b-1", AttemptID: "attempt-1"},
		Source:      metering.SourceProviderReported, Authority: metering.AuthorityAuthoritative, Presence: metering.PresencePresent,
		Quantities: []metering.Quantity{{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 12, Present: true}},
	}
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}
	obs, err := metering.ObservationFromFact(f)
	if err != nil {
		t.Fatal(err)
	}
	if obs.SourceEventKey != f.SourceEventKey() || obs.MappingRef != metering.LegacyV1MappingRef {
		t.Fatalf("legacy identity/mapping changed: obs=%+v factkey=%q", obs, f.SourceEventKey())
	}
	projected, err := metering.ProjectObservationToFact(obs)
	if err != nil {
		t.Fatal(err)
	}
	if projected.FactID != f.FactID || projected.StreamID != f.StreamID || projected.Quantities[0].Value != 12 {
		t.Fatalf("lossless projection=%+v", projected)
	}
	if _, err := metering.ObservationFromFact(projected); !errors.Is(err, metering.ErrV1Projection) {
		t.Fatalf("V1 projection must not re-enter V2 authority, err=%v", err)
	}
	nonInteger := obs
	nonInteger.Measures = []metering.Measure{{Key: mediaKey(metering.DirectionInput, metering.UnitSecond), Value: decimalPtr("1.5"), Quality: metering.QualityObserved}}
	if _, err := metering.ProjectObservationToFact(nonInteger); err == nil {
		t.Fatal("non-integer native measure must not be projected to V1 token/count DTO")
	}
	f.Money = &metering.MoneyObservation{Present: false}
	obs, err = metering.ObservationFromFact(f)
	if err != nil {
		t.Fatal(err)
	}
	projected, err = metering.ProjectObservationToFact(obs)
	if err != nil || projected.Money == nil || projected.Money.Present {
		t.Fatalf("absent V1 money changed across bridge: money=%+v err=%v", projected.Money, err)
	}
	f.Money = &metering.MoneyObservation{NanoUnits: 0, Currency: "USD", Present: true}
	obs, err = metering.ObservationFromFact(f)
	if err != nil {
		t.Fatal(err)
	}
	projected, err = metering.ProjectObservationToFact(obs)
	if err != nil || projected.Money == nil || !projected.Money.Present || projected.Money.NanoUnits != 0 {
		t.Fatalf("explicit V1 zero money changed across bridge: money=%+v err=%v", projected.Money, err)
	}
}

func validV2Observation(t *testing.T) metering.Observation {
	t.Helper()
	now := time.Unix(1_700_000_000, 0).UTC()
	return metering.Observation{
		Version: 2, ID: "obs-1", SourceEventKey: "event-1", Revision: 1, StreamID: "stream-1", Sequence: 1,
		Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject:     metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "store-1", ALegID: "a-1", BillingCallID: "call-1", BLegID: "b-1"},
		Correlation: metering.CorrelationV2{StoreID: "store-1", RequestID: "req-1", CallID: "call-1", BillingCallID: "call-1", ALegID: "a-1", BLegID: "b-1", AttemptID: "attempt-1"},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "provider:test:v1",
		Measures: []metering.Measure{{Key: mediaKey(metering.DirectionInput, metering.UnitSecond), Value: decimalPtr("1"), Quality: metering.QualityObserved}},
	}
}

func mediaKey(direction metering.FlowDirection, unit string) metering.ComponentKey {
	return metering.ComponentKey{Direction: direction, Component: "audio", Unit: unit, SchemaID: "provider:audio:v1", Dimensions: []metering.Dimension{{Name: "channels", Value: "1"}}}
}

func decimalPtr(raw string) *metering.Decimal {
	d, err := metering.ParseDecimal(raw)
	if err != nil {
		panic(err)
	}
	return &d
}
