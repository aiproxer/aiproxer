package openaichatgptplan_test

// The honest reduced reasoning-replay capability fixture (spec task 8.2).
//
// Requirements 10.4 and 10.5 make exact public Responses reasoning replay an
// OPTIONAL capability that may be advertised only after a live SIWC proof. No
// live proof exists, so the connector stays on the baseline public canonical
// history: the descriptor and every resolved profile advertise no reasoning
// replay and no reasoning dialect, and no operator key can turn an unproven
// path on.
//
// The refusal side of the same contract is pinned beside the production
// projector in internal/responses/reasoning_reduced_test.go. The opt-in live
// probe in internal/responses/reasoning_live_test.go is a RAW provider probe:
// it is not the production path and it cannot flip the capability or rewrite
// the descriptor.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestReasoningReplayCapabilityIsReducedAndUnadvertised(t *testing.T) {
	t.Parallel()
	descriptor, err := service.New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(descriptor.Factories) != 1 {
		t.Fatalf("factories = %d, want the single direct-inference export", len(descriptor.Factories))
	}
	if descriptor.Factories[0].StaticCapabilities.ReasoningReplay {
		t.Error("the static capabilities advertise exact reasoning replay without a live SIWC proof")
	}

	storePath := filepath.Join(t.TempDir(), "siwc-store")
	instance := configure(t, service.FactoryKind, "profile_store_path: "+yamlScalar(storePath)+"\n")
	t.Cleanup(func() {
		if err := instance.Close(context.Background()); err != nil {
			t.Errorf("closing the configured instance: %v", err)
		}
	})
	profile, err := instance.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Capabilities.ReasoningReplay {
		t.Error("the resolved profile advertises exact reasoning replay without a live SIWC proof")
	}
	if profile.ReasoningReplaySupported {
		t.Error("the resolved profile reports reasoning replay support without a live SIWC proof")
	}
	if len(profile.DialectSupport.ReasoningDialects) != 0 {
		t.Errorf("the resolved profile advertises a reasoning dialect without a live SIWC proof: %+v",
			profile.DialectSupport.ReasoningDialects)
	}
}

func TestNoOperatorSwitchEnablesUnprovenReasoningReplay(t *testing.T) {
	t.Parallel()
	storePath := filepath.Join(t.TempDir(), "siwc-store")
	_, err := service.New().Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID:  "test-instance",
		FactoryKind: service.FactoryKind,
		ConfigYAML:  []byte("profile_store_path: " + yamlScalar(storePath) + "\nreasoning_replay: true\n"),
	})
	if err == nil {
		t.Fatal("Configure must reject a switch that would advertise the unproven exact reasoning replay path")
	}
	if !strings.Contains(err.Error(), "reasoning_replay") {
		t.Fatalf("Configure error %q must name the rejected key %q", err, "reasoning_replay")
	}
}
