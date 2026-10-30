//go:build productsmoke

package productsmoke

import "testing"

const (
	featureSystemPromptRuleID = "smoke-product"
	featureSystemPromptText   = "Follow the product smoke steering instruction."
)

// TestProduct_Features proves opt-in feature behavior through the real binary:
// configuration reaches the shipped composition, and the client still gets an
// ordinary answer while the provider receives the feature's additions.
func TestProduct_Features(t *testing.T) {
	t.Run("model_system_prompt", testModelSystemPrompt)
}

func testModelSystemPrompt(t *testing.T) {
	t.Parallel()
	canary := newCanary(t)
	provider := newOpenAILegacyProvider(t, canary, smokeScript())
	h := startSUT(t, sutConfig{
		Canary:    canary,
		Frontends: []string{"openai-legacy"},
		Backends: []backendWiring{{
			ID:      smokeBackendID,
			Kind:    kindOpenAILegacyCompatible,
			BaseURL: provider.OpenAILegacyBaseURL(),
			Models:  []modelWiring{{Canonical: smokeCanonical, Native: smokeNative}},
		}},
		ModelSystemPrompt: &modelSystemPromptFixture{
			RuleID:       featureSystemPromptRuleID,
			ModelPattern: "^" + smokeCanonical + "$",
			Append:       featureSystemPromptText,
		},
	})
	wire := doJSON(t.Context(), h.BaseURL()+openAIChatPath, smokeRequestBody(true))
	failures := failureContext{sut: h, provider: provider, wire: wire}
	if err := checkOpenAIStream(wire, smokeScript(), h.evidence.redact); err != nil {
		failf(t, failures, "model-system-prompt: %v", err)
	}
	if got := provider.RequestCount(); got != 1 {
		failf(t, failures, "model-system-prompt: provider received %d requests, want exactly 1 (no retries)", got)
	}
	// The configured instruction is a standalone system message ahead of the
	// client's own history; the client's content is neither edited nor merged.
	wantMessages := append([]chatMessage{{Role: "system", Content: featureSystemPromptText}}, smokeMessages()...)
	if err := checkTranslatedChat(provider.Requests()[0], wantMessages, smokeTools(), h.evidence.redact); err != nil {
		failf(t, failures, "model-system-prompt: %v", err)
	}
	// The readiness probes exercise a different logical model through the same
	// binary. Exact message equality proves the rule selects rather than injects
	// everywhere: a non-matching model keeps only its own user message.
	ready := h.ReadyProvider().Requests()
	if len(ready) == 0 {
		failf(t, failures, "model-system-prompt: readiness provider recorded no requests")
	}
	readyFailures := failureContext{sut: h, provider: h.ReadyProvider(), wire: wire}
	wantReady := []chatMessage{{Role: "user", Content: readyProbeMessage}}
	for i, req := range ready {
		if err := checkTranslatedOpenAIChat(req, readyNative, openAIChatPath, wantReady, nil, h.evidence.redact); err != nil {
			failf(t, readyFailures, "model-system-prompt: readiness request %d: %v", i, err)
		}
	}
	t.Logf("model-system-prompt passed: %d provider request(s), %d readiness request(s), evidence %s",
		provider.RequestCount(), len(ready), h.evidence.dir)
}
