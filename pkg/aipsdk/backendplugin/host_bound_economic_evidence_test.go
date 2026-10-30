package backendplugin

import "testing"

// TestHostBoundEconomicEvidenceV2Negotiated pins the opt-in gate of the
// host-bound economic evidence contract: only a compatible peer that negotiated
// the new minor and advertised the new feature binds a provider claim into the
// host namespace.
func TestHostBoundEconomicEvidenceV2Negotiated(t *testing.T) {
	t.Parallel()
	opted := Negotiation{
		Compatible:      true,
		NegotiatedMinor: ProtocolMinorHostBoundEconomicEvidenceV2,
		EnabledFeatures: []string{FeatureAccountingEvidenceV2, FeatureHostBoundEconomicEvidenceV2},
	}
	if !HostBoundEconomicEvidenceV2Negotiated(opted) {
		t.Fatal("an opted-in peer was not recognized")
	}
	if HostBoundEconomicEvidenceV2Negotiated(Negotiation{
		Compatible:      true,
		NegotiatedMinor: ProtocolMinorAccountingEvidenceV2,
		EnabledFeatures: []string{FeatureAccountingEvidenceV2},
	}) {
		t.Fatal("a minor-9 peer was recognized as opted in")
	}
	if HostBoundEconomicEvidenceV2Negotiated(Negotiation{
		Compatible:      true,
		NegotiatedMinor: ProtocolMinorHostBoundEconomicEvidenceV2,
		EnabledFeatures: []string{FeatureAccountingEvidenceV2},
	}) {
		t.Fatal("a peer without the feature was recognized as opted in")
	}
	if HostBoundEconomicEvidenceV2Negotiated(Negotiation{
		Compatible:      false,
		NegotiatedMinor: ProtocolMinorHostBoundEconomicEvidenceV2,
		EnabledFeatures: []string{FeatureAccountingEvidenceV2, FeatureHostBoundEconomicEvidenceV2},
	}) {
		t.Fatal("an incompatible peer was recognized as opted in")
	}
}

// TestHostBoundEconomicEvidenceFeatureRequiresItsMinor pins the feature/minor
// table entry of the host-bound economic evidence contract.
func TestHostBoundEconomicEvidenceFeatureRequiresItsMinor(t *testing.T) {
	t.Parallel()
	host := ProtocolOffer{
		Major: ProtocolMajorV1,
		Minor: ProtocolMinorHostBoundEconomicEvidenceV2,
		Features: []Feature{
			{Name: FeatureAccountingEvidenceV2},
			{Name: FeatureHostBoundEconomicEvidenceV2},
		},
		DisableTransportRetries: true,
	}
	plugin := ProtocolOffer{
		Major: ProtocolMajorV1,
		Minor: ProtocolMinorHostBoundEconomicEvidenceV2,
		Features: []Feature{
			{Name: FeatureAccountingEvidenceV2},
			{Name: FeatureHostBoundEconomicEvidenceV2},
		},
		DisableTransportRetries: true,
	}
	neg, err := Negotiate(host, plugin)
	if err != nil {
		t.Fatalf("negotiate: %v", err)
	}
	if neg.NegotiatedMinor != ProtocolMinorHostBoundEconomicEvidenceV2 {
		t.Fatalf("negotiated minor = %d, want %d", neg.NegotiatedMinor, ProtocolMinorHostBoundEconomicEvidenceV2)
	}
	if !HostBoundEconomicEvidenceV2Negotiated(neg) {
		t.Fatal("the negotiated peer was not recognized as opted in")
	}
}
