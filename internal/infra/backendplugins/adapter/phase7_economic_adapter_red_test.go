package adapter

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// TestPhase7Adapter_V2SidebandDrainDeduplicatesFrameReplay pins the approved
// host-binding contract for a peer that did not opt into the host-bound
// economic evidence feature: the claim is never rewritten and never retained.
// The host records an explicit capture loss and the request continues.
func TestPhase7Adapter_V2SidebandDrainDeduplicatesFrameReplay(t *testing.T) {
	t.Parallel()
	s := &managedStream{
		ctx:      contextBackground(),
		opt:      Options{Negotiation: phase7Negotiation()},
		maxFrame: int(backendplugin.DefaultMaxStreamFrameBytes),
		claims:   newConnectorClaimState(execbackend.ConnectorEconomicEvidenceDegrade, false),
	}
	evidence := backendplugin.AccountingEvidenceV2{Observation: phase7AdapterObservation(), Coverage: backendplugin.EvidenceCoverageComplete}
	if err := s.onPluginFrame(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted}); err != nil {
		t.Fatalf("accepted frame: %v", err)
	}
	frame := backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccountingEvidence, Sequence: 1, AccountingV2: &evidence}
	if err := s.onPluginFrame(frame); err != nil {
		t.Fatalf("first V2 frame: %v", err)
	}
	frame.Sequence = 2
	if err := s.onPluginFrame(frame); err != nil {
		t.Fatalf("duplicate V2 frame: %v", err)
	}
	admitted, rejected := s.drainBoundClaims()
	if len(admitted) != 0 {
		t.Fatalf("drained %d observations, want none: a non-opted peer is never retained", len(admitted))
	}
	if len(rejected) != 2 {
		t.Fatalf("rejections = %d, want one per dropped claim", len(rejected))
	}
	for _, record := range rejected {
		if record.RejectionReason != string(execbackend.ConnectorEvidenceRejectedPeerNotHostBound) {
			t.Fatalf("rejection reason = %q, want %q", record.RejectionReason, execbackend.ConnectorEvidenceRejectedPeerNotHostBound)
		}
	}
}

func TestPhase8Adapter_V1SidebandDoesNotClaimCanonicalAuthority(t *testing.T) {
	t.Parallel()
	s := &managedStream{opt: Options{Negotiation: backendplugin.Negotiation{
		Compatible:      true,
		NegotiatedMinor: backendplugin.ProtocolMinorAccountingEvidence,
		EnabledFeatures: []string{backendplugin.FeatureAccountingEvidence},
	}}}
	if s.AccountingEvidenceEnabled() {
		t.Fatal("legacy V1 sideband globally suppressed canonical usage; V1 producers must project authority per source")
	}
}

func TestPhase8Adapter_V2SidebandClaimsCanonicalAuthority(t *testing.T) {
	t.Parallel()
	s := &managedStream{opt: Options{Negotiation: phase7Negotiation()}}
	if !s.AccountingEvidenceEnabled() {
		t.Fatal("negotiated V2 sideband did not claim canonical authority")
	}
}

// TestPhase7Adapter_V2ConflictIsVisibleAndFailClosed pins the approved
// host-binding contract for an opted-in peer: a changed payload under one bound
// identity is a replay/identity conflict. The first binding is preserved, the
// later claim binds nothing, and the explicit capture loss is recorded.
func TestPhase7Adapter_V2ConflictIsVisibleAndFailClosed(t *testing.T) {
	t.Parallel()
	s := &managedStream{
		ctx:           contextBackground(),
		opt:           Options{Negotiation: hostBoundNegotiation()},
		maxFrame:      int(backendplugin.DefaultMaxStreamFrameBytes),
		claims:        newConnectorClaimState(execbackend.ConnectorEconomicEvidenceDegrade, true),
		lineageBLegID: "b-adapter",
	}
	s.BindEconomicEvidence(hostBoundIdentityFor("b-adapter", "attempt-adapter"))
	first := backendplugin.AccountingEvidenceV2{Observation: phase7AdapterObservation(), Coverage: backendplugin.EvidenceCoverageComplete}
	second := first
	second.Observation.Measures = append([]metering.Measure(nil), first.Observation.Measures...)
	second.Observation.Measures[0].Value = &metering.Decimal{Coefficient: "3", Scale: 0}
	hostBoundAccept(t, s)
	for i, evidence := range []*backendplugin.AccountingEvidenceV2{&first, &second} {
		err := hostBoundSendAccountingFrame(t, s, evidence, uint64(i+1))
		if i == 0 && err != nil {
			t.Fatalf("first frame: %v", err)
		}
		if i == 1 && err != nil {
			t.Fatalf("conflicting frame: %v", err)
		}
	}
	admitted, rejected := s.drainBoundClaims()
	if len(admitted) != 1 {
		t.Fatalf("admitted observations = %d, want the first binding only", len(admitted))
	}
	if len(rejected) != 1 {
		t.Fatalf("rejections = %d, want one", len(rejected))
	}
	if rejected[0].RejectionReason != string(execbackend.ConnectorEvidenceRejectedPayloadConflict) {
		t.Fatalf("rejection reason = %q, want %q", rejected[0].RejectionReason, execbackend.ConnectorEvidenceRejectedPayloadConflict)
	}
}

// TestPhase7Adapter_V2ConflictIncludesCoverageDispositionAndRetainsCopy pins
// the coverage disposition rule of the host-binding contract: the transport
// coverage disposition of the first binding is immutable, and a repeated claim
// with a different disposition is a replay/identity conflict.
func TestPhase7Adapter_V2ConflictIncludesCoverageDispositionAndRetainsCopy(t *testing.T) {
	t.Parallel()
	s := &managedStream{
		ctx:           contextBackground(),
		opt:           Options{Negotiation: hostBoundNegotiation()},
		maxFrame:      int(backendplugin.DefaultMaxStreamFrameBytes),
		claims:        newConnectorClaimState(execbackend.ConnectorEconomicEvidenceDegrade, true),
		lineageBLegID: "b-adapter",
	}
	s.BindEconomicEvidence(hostBoundIdentityFor("b-adapter", "attempt-adapter"))
	evidence := backendplugin.AccountingEvidenceV2{Observation: phase7AdapterObservation()}
	hostBoundAccept(t, s)
	if err := hostBoundSendAccountingFrame(t, s, &evidence, 1); err != nil {
		t.Fatalf("first frame: %v", err)
	}
	// Mutating the caller-owned frame must not mutate the retained provider
	// evidence. The replay below is intentionally a coverage conflict, not a
	// changed observation payload.
	evidence.Coverage = backendplugin.EvidenceCoveragePartial
	evidence.CoverageReason = "legacy subset"
	if err := hostBoundSendAccountingFrame(t, s, &evidence, 2); err != nil {
		t.Fatalf("coverage conflict frame: %v", err)
	}
	admitted, rejected := s.drainBoundClaims()
	if len(admitted) != 1 {
		t.Fatalf("admitted observations = %d, want the first binding only", len(admitted))
	}
	if admitted[0].Measures[0].Value.Coefficient != "2" {
		t.Fatalf("retained evidence = %+v, want the copied complete original", admitted[0].Measures)
	}
	if len(rejected) != 1 {
		t.Fatalf("rejections = %d, want one", len(rejected))
	}
	if rejected[0].RejectionReason != string(execbackend.ConnectorEvidenceRejectedPayloadConflict) {
		t.Fatalf("rejection reason = %q, want %q", rejected[0].RejectionReason, execbackend.ConnectorEvidenceRejectedPayloadConflict)
	}
}

func phase7Negotiation() backendplugin.Negotiation {
	return backendplugin.Negotiation{Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorAccountingEvidenceV2, EnabledFeatures: []string{backendplugin.FeatureAccountingEvidenceV2}}
}

func phase7AdapterObservation() metering.Observation {
	now := time.Unix(1700000000, 0).UTC()
	key := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "provider.media.v2"}
	value := metering.Decimal{Coefficient: "2", Scale: 0}
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: "obs-adapter-1", SourceEventKey: "provider-event-1", Revision: 1,
		StreamID: "stream-adapter", Sequence: 1, Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject:     metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "metering", BLegID: "b-adapter", AttemptID: "attempt-adapter"},
		Correlation: metering.CorrelationV2{StoreID: "metering", BLegID: "b-adapter", AttemptID: "attempt-adapter", ProviderRequestID: "provider-request-1", ProviderAccountKey: "acct-1"},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "provider-schema-v2",
		Measures: []metering.Measure{{Key: key, Value: &value, Quality: metering.QualityObserved}},
	}
}

// contextBackground keeps this package's test fixtures independent from the
// adapter's production context ownership while remaining non-nil.
func contextBackground() context.Context { return context.Background() }

var _ aipapi.ManagedEventStream = (*managedStream)(nil)
