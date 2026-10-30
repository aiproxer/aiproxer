package openaicaps

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestForHostedModel_gpt35_dropsVision(t *testing.T) {
	t.Parallel()
	c := ForHostedModel("gpt-3.5-turbo")
	if _, ok := c[aipapi.CapabilityVision]; ok {
		t.Fatal("expected vision stripped")
	}
	if _, ok := c[aipapi.CapabilityStreaming]; !ok {
		t.Fatal("expected streaming retained")
	}
}

func TestForHostedModel_o1_dropsParallelTools(t *testing.T) {
	t.Parallel()
	c := ForHostedModel("o1-mini")
	if _, ok := c[aipapi.CapabilityParallelToolCalls]; ok {
		t.Fatal("expected parallel tool calls stripped for o-series catalog row")
	}
	if _, ok := c[aipapi.CapabilityStreaming]; !ok {
		t.Fatal("expected streaming retained")
	}
}

func TestForHostedModel_gpt4o_full(t *testing.T) {
	t.Parallel()
	c := ForHostedModel("gpt-4o-mini")
	if len(c) < len(HostedFull) {
		t.Fatalf("expected full hosted surface, got %d caps", len(c))
	}
}
