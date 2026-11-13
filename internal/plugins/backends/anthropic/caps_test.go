package anthropic_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/anthropic"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestModelCapabilities_modernClaude(t *testing.T) {
	t.Parallel()
	c := anthropic.ModelCapabilities("claude-3-5-sonnet-20241022")
	if _, ok := c[aipapi.CapabilityParallelToolCalls]; !ok {
		t.Fatal("expected parallel tool calls for modern Claude")
	}
}

func TestModelCapabilities_legacyClaude2(t *testing.T) {
	t.Parallel()
	c := anthropic.ModelCapabilities("claude-2.1")
	if _, ok := c[aipapi.CapabilityParallelToolCalls]; ok {
		t.Fatal("did not expect parallel tool calls for claude-2.x")
	}
	if _, ok := c[aipapi.CapabilityStreaming]; !ok {
		t.Fatal("expected streaming")
	}
}

func TestModelCapabilities_claudeV2Alias(t *testing.T) {
	t.Parallel()
	c := anthropic.ModelCapabilities("claude-v2.0")
	if _, ok := c[aipapi.CapabilityParallelToolCalls]; ok {
		t.Fatal("did not expect parallel tool calls for claude-v2 alias")
	}
}

// TestNew_nativeAdvertisesReasoningEffort pins that the native backend keeps a
// canonical effort instead of letting capability negotiation strip it.
func TestNew_nativeAdvertisesReasoningEffort(t *testing.T) {
	t.Parallel()
	be := anthropic.New(anthropic.Config{BaseURL: "http://127.0.0.1:1", APIKey: "k"})
	if _, ok := be.Caps[aipapi.CapabilityReasoning]; !ok {
		t.Fatal("native backend must advertise reasoning")
	}
}

// TestNew_compatibleDoesNotAdvertiseReasoningEffort pins the explicit
// degradation for upstreams that are not guaranteed to accept the field.
func TestNew_compatibleDoesNotAdvertiseReasoningEffort(t *testing.T) {
	t.Parallel()
	be := anthropic.New(anthropic.Config{BaseURL: "http://127.0.0.1:1", APIKey: "k", CompatibleModeAuth: true})
	if _, ok := be.Caps[aipapi.CapabilityReasoning]; ok {
		t.Fatal("compatible backend must not advertise reasoning")
	}
}
