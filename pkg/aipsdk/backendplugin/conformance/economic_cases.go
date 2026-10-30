package conformance

import (
	"errors"
	"fmt"
	"strings"
	"time"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// caseEconomicV2Contract is the provider-neutral sideband family case. It is
// intentionally part of RunWith so every connector gets one reusable ABI
// certification without a provider-by-provider Cartesian matrix.
func caseEconomicV2Contract() Result {
	const name = "accounting.economic_v2_transport"
	fixtures := economicV2Fixtures()
	for _, fixture := range fixtures {
		if err := fixture.Validate(); err != nil {
			return fail(name, fmt.Sprintf("fixture %s: %v", fixture.Name, err), "economic-v2")
		}
		wire, err := backendplugin.AccountingEvidenceV2ToProto(&fixture.Evidence)
		if err != nil {
			return fail(name, fmt.Sprintf("fixture %s encode: %v", fixture.Name, err), "economic-v2")
		}
		got, err := backendplugin.AccountingEvidenceV2FromProto(wire)
		if err != nil {
			return fail(name, fmt.Sprintf("fixture %s decode: %v", fixture.Name, err), "economic-v2")
		}
		if got.Observation.Fingerprint() != fixture.Evidence.Observation.Fingerprint() || got.Coverage != fixture.Evidence.Coverage {
			return fail(name, fmt.Sprintf("fixture %s changed identity", fixture.Name), "economic-v2")
		}
	}

	if err := economicV2FinalizerRoundTrip(fixtures); err != nil {
		return fail(name, err.Error(), "economic-v2")
	}
	if err := economicV2PresenceAndGraphChecks(fixtures); err != nil {
		return fail(name, err.Error(), "economic-v2")
	}
	if err := economicV2RejectsUnsafePayloads(fixtures[0].Evidence); err != nil {
		return fail(name, err.Error(), "economic-v2")
	}
	if err := economicV2NegotiationChecks(); err != nil {
		return fail(name, err.Error(), "economic-v2")
	}
	return pass(name)
}

// EconomicV2Fixture is an intentionally small connector-family fixture. The
// public accessor lets connector modules reuse the same evidence cases while
// keeping provider-shaped decoding outside the shared TCK.
type EconomicV2Fixture struct {
	Name     string
	Evidence backendplugin.AccountingEvidenceV2
}

// Validate checks the fixture label and its bounded canonical payload.
func (f EconomicV2Fixture) Validate() error {
	if strings.TrimSpace(f.Name) == "" {
		return fmt.Errorf("fixture name is required")
	}
	return f.Evidence.Validate()
}

// EconomicV2Fixtures returns deep-copied synthetic image/audio/video and
// account-window evidence for connector-level conformance tests.
func EconomicV2Fixtures() []EconomicV2Fixture {
	fixtures := economicV2Fixtures()
	for i := range fixtures {
		fixtures[i].Evidence.Observation = fixtures[i].Evidence.Observation.Clone()
	}
	return fixtures
}

func economicV2Fixtures() []EconomicV2Fixture {
	now := time.Unix(1_700_000_000, 123_000_000).UTC()
	store := "connector-tck"
	image := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "media.schema.v2"}
	audio := metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentAudio, Unit: metering.UnitSecond, SchemaID: "media.schema.v2"}
	video := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentVideo, Unit: metering.UnitFrame, SchemaID: "media.schema.v2"}
	document := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentDocument, Unit: metering.UnitDocument, SchemaID: "media.schema.v2"}
	imageValue := metering.Decimal{Coefficient: "2", Scale: 0}
	audioValue := metering.Decimal{Coefficient: "33", Scale: 1}
	videoValue := metering.Decimal{Coefficient: "120", Scale: 0}
	imageCharge := image
	imageObs := metering.Observation{
		Version: metering.ObservationVersionV2, ID: "obs-media-1", SourceEventKey: "provider-media-1", Revision: 1,
		StreamID: "stream-1", Sequence: 4, Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse,
		Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
		Lifecycle:   metering.LifecycleBackendAttempt,
		Subject:     metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: store, BLegID: "b-1", AttemptID: "attempt-1"},
		Correlation: metering.CorrelationV2{StoreID: store, BLegID: "b-1", AttemptID: "attempt-1", ProviderRequestID: "provider-request-1", ProviderAccountKey: "account-1"},
		Scope:       scope.PrincipalScopeView{SubjectKind: scope.SubjectService, PrincipalID: scope.Known("service-1")},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now.Add(time.Millisecond), MappingRef: "provider-media-schema-v2",
		Measures: []metering.Measure{
			{Key: image, Value: &imageValue, Quality: metering.QualityObserved},
			{Key: audio, Value: &audioValue, Quality: metering.QualityEstimated, MethodRef: "provider-duration"},
			{Key: video, Value: &videoValue, Quality: metering.QualityObserved},
			{Key: document, Quality: metering.QualityUnavailable, Reason: "provider did not report document count"},
		},
		Evidence: []metering.SafeEvidenceField{{Path: "$.usage.image_count", Lexeme: "2", Present: true, Acquisition: metering.AcquisitionProviderResponse}},
	}
	windowValue := metering.Decimal{Coefficient: "75", Scale: 0}
	window := metering.Observation{
		Version: metering.ObservationVersionV2, ID: "obs-window-1", SourceEventKey: "provider-window-1", Revision: 1,
		StreamID: "window-stream", Sequence: 1, Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderHeader,
		Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
		Lifecycle:   metering.LifecycleAuxiliaryRequest,
		Subject:     metering.SubjectRef{Kind: metering.SubjectAccountWindow, StoreID: store, ProviderAccountKey: "account-1", PoolID: "pool-1", WindowID: "window-1", ResetAt: now.Add(time.Hour)},
		Correlation: metering.CorrelationV2{StoreID: store, ProviderAccountKey: "account-1"},
		Semantics:   metering.SemanticsGauge, ObservedAt: now, ReceivedAt: now.Add(time.Millisecond), MappingRef: "provider-window-schema-v2",
		Measures: []metering.Measure{{Key: metering.ComponentKey{Direction: metering.DirectionNone, Component: metering.ComponentStorage, Unit: metering.UnitByteSecond}, Value: &windowValue, Quality: metering.QualityObserved}},
	}
	partial := imageObs.Clone()
	partial.ID = "obs-media-partial"
	partial.SourceEventKey = "provider-media-partial"
	partial.Revision = 1
	return []EconomicV2Fixture{
		{Name: "multimodal_native_units", Evidence: backendplugin.AccountingEvidenceV2{Observation: imageObs, Coverage: backendplugin.EvidenceCoverageComplete}},
		{Name: "account_window_gauge", Evidence: backendplugin.AccountingEvidenceV2{Observation: window, Coverage: backendplugin.EvidenceCoverageComplete}},
		{Name: "partial_observation", Evidence: backendplugin.AccountingEvidenceV2{Observation: partial, Coverage: backendplugin.EvidenceCoveragePartial, CoverageReason: "legacy-compatible subset"}},
		{Name: "charge_graph_parent", Evidence: backendplugin.AccountingEvidenceV2{Observation: economicChargeParent(imageObs, imageCharge), Coverage: backendplugin.EvidenceCoverageComplete}},
	}
}

func economicChargeParent(base metering.Observation, component metering.ComponentKey) metering.Observation {
	base.ID = "obs-charge-parent"
	base.SourceEventKey = "provider-charge-parent"
	base.Measures = nil
	amount := metering.Decimal{Coefficient: "123450", Scale: 3}
	base.Charges = []metering.ReportedCharge{{
		ChargeItemID: "provider-total", Component: &component, Amount: &amount, Currency: "USD", Kind: metering.ChargeKindComponent,
		Payer:  metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "operator-1"},
		Covers: []metering.ChargeCoverageRef{{Ref: metering.ChargeRef{StoreID: base.Subject.StoreID, ObservationID: "obs-charge-child", Revision: 1, ChargeItemID: "provider-input"}, Relation: metering.CoverageInclusive}},
	}}
	return base
}

func economicV2FinalizerRoundTrip(fixtures []EconomicV2Fixture) error {
	response := backendplugin.FinalizeBillingResponse{}
	for _, fixture := range fixtures {
		response.AccountingV2 = append(response.AccountingV2, fixture.Evidence)
	}
	wire, err := backendplugin.FinalizeBillingResponseToProto(response)
	if err != nil {
		return fmt.Errorf("finalizer encode: %v", err)
	}
	got, err := backendplugin.FinalizeBillingResponseFromProto(wire)
	if err != nil {
		return fmt.Errorf("finalizer decode: %v", err)
	}
	if len(got.AccountingV2) != len(response.AccountingV2) {
		return fmt.Errorf("finalizer evidence count=%d want %d", len(got.AccountingV2), len(response.AccountingV2))
	}
	return nil
}

func economicV2PresenceAndGraphChecks(fixtures []EconomicV2Fixture) error {
	media := fixtures[0].Evidence.Observation
	if len(media.Measures) < 4 || media.Measures[3].Value != nil || media.Measures[3].Quality != metering.QualityUnavailable {
		return fmt.Errorf("presence: unavailable media measure was not absent")
	}
	wire, err := backendplugin.EconomicObservationV2ToProto(media)
	if err != nil {
		return fmt.Errorf("presence encode: %v", err)
	}
	var unavailable *backendpluginv1.EconomicMeasureV2
	for _, measure := range wire.GetMeasures() {
		if measure.GetKey().GetComponent() == metering.ComponentDocument {
			unavailable = measure
			break
		}
	}
	if unavailable == nil || unavailable.GetValue() != nil {
		return fmt.Errorf("presence: absent measure encoded as zero")
	}
	zero := media.Clone()
	zero.Measures = append([]metering.Measure(nil), media.Measures...)
	zeroValue := metering.Decimal{Coefficient: "0", Scale: 0}
	zero.Measures[3] = metering.Measure{Key: zero.Measures[3].Key, Value: &zeroValue, Quality: metering.QualityObserved}
	zeroWire, err := backendplugin.EconomicObservationV2ToProto(zero)
	var explicitZero *backendpluginv1.EconomicMeasureV2
	for _, measure := range zeroWire.GetMeasures() {
		if measure.GetKey().GetComponent() == metering.ComponentDocument {
			explicitZero = measure
			break
		}
	}
	if err != nil || explicitZero == nil || explicitZero.GetValue() == nil || explicitZero.GetValue().GetCoefficient() != "0" {
		return fmt.Errorf("presence: explicit zero was not retained")
	}
	child := media.Clone()
	child.ID = "obs-charge-child"
	child.SourceEventKey = "provider-charge-child"
	child.Measures = nil
	child.Evidence = nil
	child.Charges = []metering.ReportedCharge{{ChargeItemID: "provider-input", Amount: &metering.Decimal{Coefficient: "100", Scale: 3}, Currency: "USD", Kind: metering.ChargeKindAggregate, Payer: metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "operator-1"}}}
	parent := fixtures[3].Evidence.Observation
	if err := metering.ValidateCoverageGraph([]metering.Observation{parent, child}); err != nil {
		return fmt.Errorf("charge coverage graph: %v", err)
	}
	prior := media.Clone()
	prior.ID = "obs-late"
	prior.SourceEventKey = "provider-late-1"
	prior.Measures = prior.Measures[:1]
	ref, err := prior.Ref(prior.Subject.StoreID)
	if err != nil {
		return fmt.Errorf("late revision ref: %v", err)
	}
	correction := prior.Clone()
	correction.SourceEventKey = "provider-late-2"
	correction.Revision = 2
	correction.Semantics = metering.SemanticsCorrection
	correction.Supersedes = []metering.ObservationRef{ref}
	if err := metering.ValidateSupersessionGraph([]metering.Observation{prior, correction}); err != nil {
		return fmt.Errorf("late revision graph: %v", err)
	}
	return nil
}

func economicV2RejectsUnsafePayloads(base backendplugin.AccountingEvidenceV2) error {
	secret := "sk_live_connector_tck_secret"
	bad := base
	bad.Observation = base.Observation.Clone()
	bad.Observation.Measures = []metering.Measure{{Key: metering.ComponentKey{Direction: metering.DirectionInput, Component: secret, Unit: metering.UnitImage}, Value: &metering.Decimal{Coefficient: "1", Scale: 0}, Quality: metering.QualityObserved}}
	err := bad.Validate()
	if err == nil || strings.Contains(err.Error(), secret) {
		return fmt.Errorf("unsafe component error was not secret-safe")
	}
	bad.Observation = base.Observation.Clone()
	bad.Observation.Charges = []metering.ReportedCharge{{ChargeItemID: "money-without-currency", Amount: &metering.Decimal{Coefficient: "1", Scale: 2}, Kind: metering.ChargeKindAggregate}}
	if _, err := backendplugin.AccountingEvidenceV2ToProto(&bad); err == nil {
		return fmt.Errorf("unsupported monetary detail was accepted")
	}
	bad = base
	bad.CoverageReason = strings.Repeat("x", int(backendplugin.DefaultMaxDiagnosticBytes)+1)
	if err := bad.Validate(); err == nil {
		return fmt.Errorf("oversized coverage reason was accepted")
	}
	if _, err := backendplugin.EconomicDecimalV2FromProto(&backendpluginv1.EconomicDecimalV2{Coefficient: "1", Scale: 256}); err == nil {
		return fmt.Errorf("wide decimal scale was truncated")
	}
	if _, err := backendplugin.EconomicObservationV2FromProto(&backendpluginv1.EconomicObservationV2{}); err == nil {
		return fmt.Errorf("empty typed observation was accepted")
	}
	return nil
}

func economicV2NegotiationChecks() error {
	newOffer := backendplugin.ProtocolOffer{Major: 1, Minor: backendplugin.ProtocolMinorAccountingEvidenceV2, DisableTransportRetries: true, Features: []backendplugin.Feature{{Name: backendplugin.FeatureAccountingEvidenceV2, Required: true}}}
	neg, err := backendplugin.Negotiate(newOffer, newOffer)
	if err != nil || !backendplugin.AccountingEvidenceV2Negotiated(neg) {
		return fmt.Errorf("new/new negotiation: %v", err)
	}
	oldOffer := backendplugin.ProtocolOffer{Major: 1, Minor: backendplugin.ProtocolMinorAccountingEvidence, DisableTransportRetries: true}
	if _, err := backendplugin.Negotiate(newOffer, oldOffer); err == nil {
		return fmt.Errorf("strict new/old negotiation unexpectedly succeeded")
	}
	if err := backendplugin.RequireAccountingEvidenceV2(backendplugin.Negotiation{Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorAccountingEvidence}); !errors.Is(err, backendplugin.ErrAccountingEvidenceV2Unsupported) {
		return fmt.Errorf("unsupported negotiation error=%v", err)
	}
	return nil
}
