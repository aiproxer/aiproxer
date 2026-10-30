package providerprofiles_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/providerprofiles"
)

// RED: new strict-provider quirks must validate fail-closed by family.
func TestStrictQuirks_ValidateFailClosed(t *testing.T) {
	t.Parallel()
	chatQuirk := providerprofiles.QuirkID("openai.chat.reasoning_content_on_tool_calls")
	responsesQuirk := providerprofiles.QuirkID("openai.responses.tool_output_adjacency")

	baseChat := providerprofiles.Profile{
		APIVersion: providerprofiles.APIVersionV1,
		ID:         "red-chat",
		Family:     providerprofiles.FamilyOpenAIChat,
		Endpoint:   providerprofiles.Endpoint{BaseURL: "https://api.deepseek.com", PathPolicy: providerprofiles.PathPolicyFamilyDefault},
		Auth:       providerprofiles.Auth{Mode: providerprofiles.AuthBearerEnv, EnvVar: "DEEPSEEK_API_KEY"},
		Models:     providerprofiles.ModelDiscovery{Policy: providerprofiles.DiscoveryFamilyDefault, Namespace: providerprofiles.Namespace{Mode: providerprofiles.NamespacePreserve}},
	}
	baseResponses := baseChat
	baseResponses.ID = "red-responses"
	baseResponses.Family = providerprofiles.FamilyOpenAIResponses
	baseResponses.Models.Policy = providerprofiles.DiscoveryStatic
	baseResponses.Models.Static = []providerprofiles.Model{{CanonicalID: "m", NativeID: "m", DisplayName: "M"}}

	// Chat quirk accepted on chat family.
	p := baseChat
	p.Quirks = []providerprofiles.QuirkID{chatQuirk}
	if err := providerprofiles.Validate(p); err != nil {
		t.Fatalf("RED: chat quirk must validate on chat family: %v", err)
	}
	// Chat quirk rejected on responses family.
	p = baseResponses
	p.Quirks = []providerprofiles.QuirkID{chatQuirk}
	if err := providerprofiles.Validate(p); err == nil {
		t.Fatalf("RED: chat quirk must be rejected on responses family")
	}
	// Responses quirk accepted on responses family.
	p = baseResponses
	p.Quirks = []providerprofiles.QuirkID{responsesQuirk}
	if err := providerprofiles.Validate(p); err != nil {
		t.Fatalf("RED: responses quirk must validate on responses family: %v", err)
	}
	// Responses quirk rejected on chat family.
	p = baseChat
	p.Quirks = []providerprofiles.QuirkID{responsesQuirk}
	if err := providerprofiles.Validate(p); err == nil {
		t.Fatalf("RED: responses quirk must be rejected on chat family")
	}
}
