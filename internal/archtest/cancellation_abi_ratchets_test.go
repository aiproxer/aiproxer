package archtest

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/contracttest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/contract"
)

// TestArch_CancellationHandshake_ABIPolicy validates the ABI policy for cancellation handshake constants and modes.
func TestArch_CancellationHandshake_ABIPolicy(t *testing.T) {
	t.Parallel()

	// 1. Validate exact minor version and feature name
	if backendplugin.ProtocolMinorCancellationHandshake != 8 {
		t.Fatalf("ProtocolMinorCancellationHandshake = %d, want 8", backendplugin.ProtocolMinorCancellationHandshake)
	}
	if backendplugin.FeatureCancellationHandshake != "cancellation_handshake_v1" {
		t.Fatalf("FeatureCancellationHandshake = %q, want %q", backendplugin.FeatureCancellationHandshake, "cancellation_handshake_v1")
	}

	// 2. Validate ABI mutation policy allows the exact constants
	validMinorSymbol := PublicABISymbol{
		Category: "const",
		Name:     "ProtocolMinorCancellationHandshake",
		Detail:   "ProtocolMinorCancellationHandshake uint32 = 8",
	}
	if err := ValidatePublicBackendPluginABIMutation([]PublicABISymbol{validMinorSymbol}); err != nil {
		t.Fatalf("expected valid ProtocolMinorCancellationHandshake to pass ABI mutation validation: %v", err)
	}

	// 3. Validate ABI mutation policy rejects mutated/near-miss values
	mutatedMinorSymbol := PublicABISymbol{
		Category: "const",
		Name:     "ProtocolMinorCancellationHandshake",
		Detail:   "ProtocolMinorCancellationHandshake uint32 = 99",
	}
	if err := ValidatePublicBackendPluginABIMutation([]PublicABISymbol{mutatedMinorSymbol}); err == nil {
		t.Fatal("expected mutated ProtocolMinorCancellationHandshake to be rejected by ABI mutation validation")
	}

	// 4. Validate known cancellation modes
	validModes := []backendplugin.CancelMode{
		backendplugin.CancelModeNone,
		backendplugin.CancelModeProvider,
		backendplugin.CancelModeTransport,
		backendplugin.CancelModeCloseOnly,
	}
	for _, mode := range validModes {
		if err := backendplugin.ValidateCancelMode(mode); err != nil {
			t.Errorf("ValidateCancelMode(%q) failed: %v", mode, err)
		}
	}
}

func TestArch_ContractTCK_ActiveCancellationCertified(t *testing.T) {
	t.Parallel()

	corpus := contract.BaselineScenarioCorpus()
	var foundCancellation bool
	for _, sc := range corpus {
		if sc.Feature == contract.FeatureCancellation {
			foundCancellation = true
			break
		}
	}
	if !foundCancellation {
		t.Fatal("contract.BaselineScenarioCorpus() must include FeatureCancellation scenario")
	}

	result := contracttest.CertificationResult{
		PluginID: "test-plugin",
		Version:  "1.0.0",
		Negotiated: backendplugin.Negotiation{
			Compatible:      true,
			NegotiatedMinor: backendplugin.ProtocolMinorCancellationHandshake,
			EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake},
		},
		ScenarioResults: make([]contracttest.ScenarioResult, len(corpus)),
	}
	for i, sc := range corpus {
		result.ScenarioResults[i] = contracttest.ScenarioResult{
			ID:              string(sc.ID),
			Positive:        true,
			Executed:        true,
			FramesValidated: 1,
			Terminal:        true,
			Cancelled:       sc.Feature == contract.FeatureCancellation,
		}
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("expected valid certification result to pass: %v", err)
	}
}
