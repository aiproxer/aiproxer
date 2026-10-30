package openaicaps_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicaps"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestForHostedModel_gpt35DropsVisionAndDocuments(t *testing.T) {
	t.Parallel()
	c := openaicaps.ForHostedModel("gpt-3.5-turbo")
	if _, ok := c[aipapi.CapabilityVision]; ok {
		t.Fatal("expected vision stripped for gpt-3.5")
	}
	if _, ok := c[aipapi.CapabilityDocuments]; ok {
		t.Fatal("expected documents stripped for gpt-3.5")
	}
	if _, ok := c[aipapi.CapabilityTools]; !ok {
		t.Fatal("expected tools retained")
	}
}

func TestForHostedModel_gpt4oKeepsMultimodal(t *testing.T) {
	t.Parallel()
	c := openaicaps.ForHostedModel("gpt-4o-mini")
	if _, ok := c[aipapi.CapabilityVision]; !ok {
		t.Fatal("expected vision for gpt-4o-mini")
	}
}

func TestForHostedModel_emptyModelReturnsFull(t *testing.T) {
	t.Parallel()
	c := openaicaps.ForHostedModel("")
	if len(c) != len(openaicaps.HostedFull) {
		t.Fatalf("got %d caps, want %d", len(c), len(openaicaps.HostedFull))
	}
}
