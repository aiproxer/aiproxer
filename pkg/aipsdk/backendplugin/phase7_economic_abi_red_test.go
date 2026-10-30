package backendplugin_test

import (
	"errors"
	"testing"
	"time"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestPhase7EconomicABI_V2ObservationRoundTripPreservesTypedEvidence(t *testing.T) {
	t.Parallel()

	want := backendplugin.AccountingEvidenceV2{
		Observation: phase7Observation(),
		Coverage:    backendplugin.EvidenceCoverageComplete,
	}
	wire, err := backendplugin.AccountingEvidenceV2ToProto(&want)
	if err != nil {
		t.Fatalf("to proto: %v", err)
	}
	if gotSize := proto.Size(wire); gotSize == 0 || gotSize > int(backendplugin.DefaultMaxEconomicObservationBytes) {
		t.Fatalf("wire size=%d outside bounded V2 observation size", gotSize)
	}
	got, err := backendplugin.AccountingEvidenceV2FromProto(wire)
	if err != nil {
		t.Fatalf("from proto: %v", err)
	}
	if got.Observation.Fingerprint() != want.Observation.Fingerprint() {
		t.Fatalf("observation fingerprint changed: got=%s want=%s", got.Observation.Fingerprint(), want.Observation.Fingerprint())
	}
	if got.Observation.Measures[0].Key.Direction != metering.DirectionInput || got.Observation.Measures[0].Key.Unit != metering.UnitImage {
		t.Fatalf("multimodal key lost: %+v", got.Observation.Measures[0].Key)
	}
	if len(got.Observation.Charges) != 1 || got.Observation.Charges[0].Amount == nil || got.Observation.Charges[0].Amount.CanonicalString() != "12345/2" {
		t.Fatalf("exact charge lost: %+v", got.Observation.Charges)
	}
}

func TestPhase7EconomicABI_PresenceAndUnsafeBoundsFailClosed(t *testing.T) {
	t.Parallel()

	zero := &backendpluginv1.EconomicDecimalV2{Coefficient: "0", Scale: 0}
	if _, err := backendplugin.EconomicDecimalV2FromProto(zero); err != nil {
		t.Fatalf("explicit zero should be representable: %v", err)
	}
	bad := &backendpluginv1.EconomicDecimalV2{Coefficient: "1.2", Scale: 1}
	if _, err := backendplugin.EconomicDecimalV2FromProto(bad); err == nil {
		t.Fatal("unexpected decimal type/lexeme was accepted")
	} else if errors.Is(err, backendplugin.ErrInvalidFrame) && len(err.Error()) > 512 {
		t.Fatalf("unsafe error was not bounded: %v", err)
	}
	oversized := phase7Observation()
	oversized.Evidence = []metering.SafeEvidenceField{{
		Path: "$.usage.safe", Lexeme: string(make([]byte, metering.MaxSafeEvidenceFieldBytes+1)), Present: true,
		Acquisition: metering.AcquisitionProviderResponse,
	}}
	if _, err := backendplugin.AccountingEvidenceV2ToProto(&backendplugin.AccountingEvidenceV2{Observation: oversized}); err == nil {
		t.Fatal("oversized safe evidence was accepted")
	}
}

func TestPhase7EconomicABI_NegotiationAndCapabilityAreExplicit(t *testing.T) {
	t.Parallel()
	host := backendplugin.ProtocolOffer{
		Major: 1, Minor: backendplugin.ProtocolMinorAccountingEvidenceV2,
		DisableTransportRetries: true,
		Features:                []backendplugin.Feature{{Name: backendplugin.FeatureAccountingEvidenceV2, Required: true}},
	}
	peer := host
	neg, err := backendplugin.Negotiate(host, peer)
	if err != nil || !backendplugin.AccountingEvidenceV2Negotiated(neg) {
		t.Fatalf("V2 negotiation failed: neg=%+v err=%v", neg, err)
	}
	old := backendplugin.ProtocolOffer{Major: 1, Minor: backendplugin.ProtocolMinorAccountingEvidence, DisableTransportRetries: true}
	if _, err := backendplugin.Negotiate(host, old); !errors.Is(err, backendplugin.ErrUnknownRequiredFeature) && !errors.Is(err, backendplugin.ErrIncompatibleMinor) {
		t.Fatalf("strict V2 offer against old peer was not rejected: %v", err)
	}
	if backendplugin.AccountingEvidenceV2Negotiated(backendplugin.Negotiation{
		Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorAccountingEvidenceV2,
	}) {
		t.Fatal("V2 capability was inferred without the negotiated feature")
	}
}

func TestPhase7EconomicABI_V2SidebandRetainsHostOnlyFrameShape(t *testing.T) {
	t.Parallel()
	evidence := &backendplugin.AccountingEvidenceV2{Observation: phase7Observation()}
	frame := backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccountingEvidence, AccountingV2: evidence}
	if err := frame.ValidateShape(); err != nil {
		t.Fatalf("V2 frame shape: %v", err)
	}
	wire, err := backendplugin.ServerFrameToProto(frame)
	if err != nil {
		t.Fatalf("frame to proto: %v", err)
	}
	if wire.GetAccountingEvidenceV2() == nil || wire.GetAccountingEvidence() != nil {
		t.Fatalf("V2 frame was not isolated from V1 accounting payload")
	}
	got, err := backendplugin.ServerFrameFromProto(wire)
	if err != nil {
		t.Fatalf("frame from proto: %v", err)
	}
	if got.AccountingV2 == nil || got.AccountingV2.Observation.ID != evidence.Observation.ID {
		t.Fatalf("V2 frame evidence lost: %+v", got.AccountingV2)
	}
}

func TestPhase7EconomicABI_PreservesV1TagsAndAddsOnlyNewTags(t *testing.T) {
	t.Parallel()
	assertFieldNumber := func(t *testing.T, message protoreflect.MessageDescriptor, name protoreflect.Name, want protoreflect.FieldNumber) {
		t.Helper()
		field := message.Fields().ByName(name)
		if field == nil || field.Number() != want {
			t.Fatalf("%s.%s number=%v want %d", message.FullName(), name, fieldNumber(field), want)
		}
	}
	accounting := (&backendpluginv1.AccountingEvidence{}).ProtoReflect().Descriptor()
	for name, number := range map[protoreflect.Name]protoreflect.FieldNumber{
		"input_tokens": 1, "output_tokens": 2, "cache_read_tokens": 3, "cache_write_tokens": 4,
		"reasoning_tokens": 5, "total_tokens": 6, "presence": 7, "source": 8, "authority": 9,
		"plane": 10, "dedupe_key": 11,
	} {
		assertFieldNumber(t, accounting, name, number)
	}
	frame := (&backendpluginv1.ExecuteServerFrame{}).ProtoReflect().Descriptor()
	assertFieldNumber(t, frame, "accounting_evidence", 7)
	assertFieldNumber(t, frame, "prompt_cache_observation", 8)
	assertFieldNumber(t, frame, "accounting_evidence_v2", 9)
	finalizer := (&backendpluginv1.FinalizeBillingResponse{}).ProtoReflect().Descriptor()
	assertFieldNumber(t, finalizer, "usage", 1)
	assertFieldNumber(t, finalizer, "evidence_quality", 2)
	assertFieldNumber(t, finalizer, "accounting_evidence_v2", 3)
}

func TestPhase7EconomicABI_UnknownV2WireFieldsFailClosed(t *testing.T) {
	t.Parallel()
	wire, err := backendplugin.EconomicObservationV2ToProto(phase7Observation())
	if err != nil {
		t.Fatalf("encode observation: %v", err)
	}
	wire.Measures[0].Key.ProtoReflect().SetUnknown([]byte{0x98, 0x06, 0x01})
	if _, err := backendplugin.EconomicObservationV2FromProto(wire); err == nil {
		t.Fatal("unknown nested V2 field was silently discarded")
	} else if errors.Is(err, backendplugin.ErrInvalidFrame) && len(err.Error()) > 512 {
		t.Fatalf("unknown-field error was not bounded: %v", err)
	}
}

func fieldNumber(field protoreflect.FieldDescriptor) protoreflect.FieldNumber {
	if field == nil {
		return 0
	}
	return field.Number()
}

func phase7Observation() metering.Observation {
	now := time.Unix(1700000000, 123000000).UTC()
	inputImage := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "provider.media.v2"}
	return metering.Observation{
		Version: metering.ObservationVersionV2,
		ID:      "obs-phase7-1", SourceEventKey: "provider-event-1", Revision: 1,
		StreamID: "stream-phase7", Sequence: 4,
		Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse,
		Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator,
		Boundary: metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject:     metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "metering", BLegID: "b-phase7", AttemptID: "attempt-phase7"},
		Correlation: metering.CorrelationV2{StoreID: "metering", BLegID: "b-phase7", AttemptID: "attempt-phase7", ProviderRequestID: "provider-request-1", ProviderAccountKey: "acct-1"},
		Scope:       scope.PrincipalScopeView{SubjectKind: scope.SubjectService, PrincipalID: scope.Known("service-1")},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now.Add(time.Millisecond), MappingRef: "provider-schema-v2",
		Measures: []metering.Measure{{Key: inputImage, Value: &metering.Decimal{Coefficient: "2", Scale: 0}, Quality: metering.QualityObserved}},
		Charges:  []metering.ReportedCharge{{ChargeItemID: "charge-1", Component: &inputImage, Amount: &metering.Decimal{Coefficient: "123450", Scale: 3}, Currency: "USD", Kind: metering.ChargeKindComponent}},
		Evidence: []metering.SafeEvidenceField{{Path: "$.usage.image_count", Lexeme: "2", Present: true, Acquisition: metering.AcquisitionProviderResponse}},
	}
}
