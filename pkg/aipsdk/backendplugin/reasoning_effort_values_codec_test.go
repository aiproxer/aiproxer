package backendplugin

import (
	"strings"
	"testing"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
)

func TestEffortValuesProtoRoundTrip(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		decl *ReasoningEffortValuesV1
	}{
		{"supported exhaustive", &ReasoningEffortValuesV1{ModelID: "gpt-6-astra", Supported: []string{"low", "medium", "high", "xhigh", "max"}, Exhaustive: true}},
		{"partial", &ReasoningEffortValuesV1{ModelID: "m", Supported: []string{"low"}}},
		{"empty exhaustive", &ReasoningEffortValuesV1{ModelID: "m", Exhaustive: true}},
		{"nil absent", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wire, err := reasoningEffortValuesToProto(tc.decl)
			if err != nil {
				t.Fatalf("to proto: %v", err)
			}
			back, err := reasoningEffortValuesFromProto(wire)
			if err != nil {
				t.Fatalf("from proto: %v", err)
			}
			if tc.decl == nil {
				if back != nil {
					t.Fatalf("nil must round-trip to nil")
				}
				return
			}
			if back.ModelID != tc.decl.ModelID || back.Exhaustive != tc.decl.Exhaustive || len(back.Supported) != len(tc.decl.Supported) {
				t.Fatalf("round-trip mismatch: %+v vs %+v", back, tc.decl)
			}
		})
	}
	profile := ResolvedProfile{
		Capabilities:          CapabilitySummary{Streaming: true},
		ReasoningEffortValues: &ReasoningEffortValuesV1{ModelID: "gpt-6-astra", Supported: []string{"high"}, Exhaustive: true},
	}
	wire := ResolvedProfileToProto(profile)
	if wire == nil || wire.GetReasoningEffortValues() == nil {
		t.Fatalf("profile proto must carry effort values")
	}
	back, err := ResolvedProfileFromProto(wire)
	if err != nil {
		t.Fatalf("profile from proto: %v", err)
	}
	if back.ReasoningEffortValues == nil || back.ReasoningEffortValues.ModelID != "gpt-6-astra" {
		t.Fatalf("profile round-trip lost effort values: %+v", back.ReasoningEffortValues)
	}
	// Nil model-agnostic profile leaves the field absent.
	bare := ResolvedProfile{Capabilities: CapabilitySummary{Streaming: true}}
	if w := ResolvedProfileToProto(bare); w.GetReasoningEffortValues() != nil {
		t.Fatalf("absent declaration must stay absent on the wire")
	}
}

func TestEffortValuesProtoRejectsInvalid(t *testing.T) {
	t.Parallel()
	bad := []*backendpluginv1.ReasoningEffortValuesV1{
		{ModelId: "", Supported: []string{"low"}},
		{ModelId: "m", Supported: []string{"low", "low"}},
		{ModelId: "m", Supported: []string{""}},
		{ModelId: "m", Supported: []string{"hi\x01"}},
		{ModelId: "m", Supported: []string{strings.Repeat("a", 65)}},
		{ModelId: "m\x7f"},
	}
	for i, wire := range bad {
		if _, err := reasoningEffortValuesFromProto(wire); err == nil {
			t.Fatalf("case %d: invalid wire admitted", i)
		}
		if _, err := ResolvedProfileFromProto(&backendpluginv1.ResolvedProfile{ReasoningEffortValues: wire}); err == nil {
			t.Fatalf("case %d: invalid profile admitted", i)
		}
	}
	// Oversized count rejected.
	many := make([]string, MaxReasoningEffortValues+1)
	for i := range many {
		many[i] = "v" + string(rune('a'+i%26)) + string(rune('0'+i/26)) + string(rune('A'+i%26))
	}
	if _, err := reasoningEffortValuesFromProto(&backendpluginv1.ReasoningEffortValuesV1{ModelId: "m", Supported: many}); err == nil {
		t.Fatalf("oversized count admitted")
	}
	// An invalid DTO cannot be represented: ToProto returns nil and the
	// server reports the profile as InvalidArgument.
	if got := ResolvedProfileToProto(ResolvedProfile{ReasoningEffortValues: &ReasoningEffortValuesV1{}}); got != nil {
		t.Fatalf("invalid DTO must not encode a profile")
	}
}

func TestEffortValuesNegotiationMatrix(t *testing.T) {
	t.Parallel()
	neg11 := Negotiation{Compatible: true, NegotiatedMinor: ProtocolMinorReasoningEffortValues, EnabledFeatures: []string{FeatureReasoningEffortValues}}
	if !ReasoningEffortValuesNegotiated(neg11) {
		t.Fatalf("11+feature must enable")
	}
	// Feature present but minor too low never enables.
	low := Negotiation{Compatible: true, NegotiatedMinor: ProtocolMinorReasoningEffortValues - 1, EnabledFeatures: []string{FeatureReasoningEffortValues}}
	if ReasoningEffortValuesNegotiated(low) {
		t.Fatalf("feature with low minor must not enable")
	}
	// Minor 11 without the feature never enables (10↔11 legacy peer).
	legacy := Negotiation{Compatible: true, NegotiatedMinor: ProtocolMinorReasoningEffortValues, EnabledFeatures: []string{FeatureHostBoundEconomicEvidenceV2}}
	if ReasoningEffortValuesNegotiated(legacy) {
		t.Fatalf("minor 11 without feature must not enable")
	}
	// 10↔11 old peer: negotiated 10 without feature stays Unknown-capable.
	old := Negotiation{Compatible: true, NegotiatedMinor: ProtocolMinorHostBoundEconomicEvidenceV2, EnabledFeatures: []string{FeatureHostBoundEconomicEvidenceV2}}
	if ReasoningEffortValuesNegotiated(old) {
		t.Fatalf("minor-10 peer must not enable effort values")
	}
}
