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
