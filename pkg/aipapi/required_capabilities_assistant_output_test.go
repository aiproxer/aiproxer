package aipapi_test

import (
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// P2.3 (llm-api-parity): assistant multimodal *response* refs are canonical events only;
// capability negotiation still keys off request shape (RequiredCapabilities), not output events.
func TestRequiredCapabilities_textOnlyUserAndAssistantParts(t *testing.T) {
	t.Parallel()
	c := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("ping")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("pong")}},
		},
	}
	req := aipapi.RequiredCapabilities(c)
	if slices.Contains(req, aipapi.CapabilityVision) || slices.Contains(req, aipapi.CapabilityDocuments) {
		t.Fatalf("unexpected multimodal caps for text-only messages: %v", req)
	}
}
