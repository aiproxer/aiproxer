package openaicaps_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicaps"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestHostedFull_doesNotAdvertiseReasoningReplay_RED(t *testing.T) {
	t.Parallel()
	if _, ok := openaicaps.HostedFull[aipapi.CapabilityReasoningReplay]; ok {
		t.Fatal("RED: HostedFull must not broadly advertise CapabilityReasoningReplay")
	}
}

func TestForHostedModel_gpt35NoReasoningReplay_RED(t *testing.T) {
	t.Parallel()
	c := openaicaps.ForHostedModelCompatibleReplay("gpt-3.5-turbo", []string{"openai-legacy"})
	if _, ok := c[aipapi.CapabilityReasoningReplay]; ok {
		t.Fatal("RED: gpt-3.5 must not claim CapabilityReasoningReplay")
	}
}

func TestForHostedModel_gpt4oNoReasoningReplay_RED(t *testing.T) {
	t.Parallel()
	c := openaicaps.ForHostedModelCompatibleReplay("gpt-4o-mini", []string{"openai-legacy"})
	if _, ok := c[aipapi.CapabilityReasoningReplay]; ok {
		t.Fatal("RED: unrelated OpenAI models must not claim CapabilityReasoningReplay")
	}
}

func TestForHostedModel_kimiMoonshotClaimsReasoningReplay_RED(t *testing.T) {
	t.Parallel()
	cases := []struct {
		model    string
		prefixes []string
	}{
		{model: "moonshotai/kimi-k2", prefixes: []string{"openrouter"}},
		{model: "moonshot-v1-8k", prefixes: []string{"openai-legacy"}},
		{model: "kimi-k2-preview", prefixes: []string{"openai-responses"}},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			t.Parallel()
			c := openaicaps.ForHostedModelCompatibleReplay(tc.model, tc.prefixes)
			if _, ok := c[aipapi.CapabilityReasoningReplay]; !ok {
				t.Fatalf("RED: proven catalog model %q prefix %v must claim CapabilityReasoningReplay", tc.model, tc.prefixes)
			}
		})
	}
}

func TestForHostedModel_kimiOnUnlistedPrefixNoReasoningReplay_RED(t *testing.T) {
	t.Parallel()
	c := openaicaps.ForHostedModelCompatibleReplay("moonshotai/kimi-k2", []string{"not-a-catalog-family"})
	if _, ok := c[aipapi.CapabilityReasoningReplay]; ok {
		t.Fatal("RED: kimi/moonshot on non-catalog family prefix must not claim CapabilityReasoningReplay")
	}
}
