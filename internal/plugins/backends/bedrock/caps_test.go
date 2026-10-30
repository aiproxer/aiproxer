package bedrock_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/bedrock"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestModelCapabilities_anthropicSonnet(t *testing.T) {
	t.Parallel()
	c := bedrock.ModelCapabilities("arn:aws:bedrock:us-east-1::foundation-model/anthropic.claude-3-5-sonnet-20240620-v1:0")
	if _, ok := c[aipapi.CapabilityParallelToolCalls]; !ok {
		t.Fatal("expected parallel tool calls")
	}
}

func TestModelCapabilities_anthropicClaude2OnBedrock(t *testing.T) {
	t.Parallel()
	c := bedrock.ModelCapabilities("anthropic.claude-v2")
	if _, ok := c[aipapi.CapabilityParallelToolCalls]; ok {
		t.Fatal("did not expect parallel tool calls for claude-v2")
	}
}

func TestModelCapabilities_titanText(t *testing.T) {
	t.Parallel()
	c := bedrock.ModelCapabilities("amazon.titan-text-express-v1")
	if _, ok := c[aipapi.CapabilityStreaming]; !ok {
		t.Fatal("expected streaming only baseline")
	}
	if len(c) != 1 {
		t.Fatalf("expected single capability, got %d", len(c))
	}
}
