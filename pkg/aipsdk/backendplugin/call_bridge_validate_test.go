package backendplugin_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestInvocationValidate_UsesCallFromInvocationMapper(t *testing.T) {
	t.Parallel()
	inv := backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "al", BLegID: "bl", CanonicalModelID: "m",
		PromptCacheKey: "legacy",
		SemanticExtensions: []backendplugin.SemanticExtension{{
			Namespace: "aip", Type: "prompt_cache_key", Implementor: "proxy", Direction: "request",
			Presence: backendplugin.SemanticExtensionValue,
			Data:     backendplugin.RawJSONFromBytes([]byte(`"carrier"`)),
		}},
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleUser,
			Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: strPtr("hello")}},
		}},
	}

	validateErr := inv.Validate()
	call, mapErr := backendplugin.CallFromInvocation(inv)
	if mapErr == nil {
		t.Fatalf("CallFromInvocation accepted conflicting prompt-cache authorities: %#v", call)
	}
	if validateErr == nil {
		t.Fatal("Validate accepted conflicting prompt-cache authorities that CallFromInvocation rejects")
	}
	if !strings.Contains(mapErr.Error(), "prompt_cache_key") {
		t.Fatalf("CallFromInvocation error %q does not mention prompt_cache_key", mapErr)
	}
	if !strings.Contains(validateErr.Error(), "prompt_cache_key") {
		t.Fatalf("Validate error %q does not mention prompt_cache_key", validateErr)
	}
	if errors.Is(validateErr, backendplugin.ErrInvalidInvocation) != errors.Is(mapErr, backendplugin.ErrInvalidInvocation) {
		t.Fatalf("error identity diverged: validate=%v map=%v", validateErr, mapErr)
	}
}

func TestInvocationValidate_MatchesCallFromInvocationSuccess(t *testing.T) {
	t.Parallel()
	inv := backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "al", BLegID: "bl", CanonicalModelID: "m",
		ProxyOwnedSessionID: "sess-1",
		PromptCacheKey:      "cache-1",
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleUser,
			Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: strPtr("hello")}},
		}},
	}
	if err := inv.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	call, err := backendplugin.CallFromInvocation(inv)
	if err != nil {
		t.Fatalf("CallFromInvocation: %v", err)
	}
	if call.Session.AuthoritativeSessionID != "sess-1" {
		t.Fatalf("session authority dropped: %q", call.Session.AuthoritativeSessionID)
	}
	if call.PromptCacheKey != "cache-1" {
		t.Fatalf("prompt cache dropped: %q", call.PromptCacheKey)
	}
}

func TestCallFromInvocation_ItemAuthorityClearsLegacyMessages(t *testing.T) {
	t.Parallel()
	inv := backendplugin.Invocation{
		RequestID: "req", AttemptID: "att", ALegID: "a", BLegID: "b", CanonicalModelID: "m",
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleUser,
			Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: strPtr("legacy")}},
		}},
		ItemAuthority: true,
		Items: []backendplugin.InvocationItem{{
			Kind:   string(aipapi.ItemKindMessage),
			ID:     "m1",
			Status: string(aipapi.ItemStatusCompleted),
			Role:   aipapi.RoleUser,
			Content: []backendplugin.InvocationContentPart{{
				Kind: backendplugin.PartKindText,
				Text: strPtr("hello"),
			}},
		}},
	}
	call, err := backendplugin.CallFromInvocation(inv)
	if err != nil {
		t.Fatalf("CallFromInvocation: %v", err)
	}
	if len(call.Messages) != 0 {
		t.Fatalf("legacy messages leaked onto item-authority call: %#v", call.Messages)
	}
	if len(call.Items) != 1 || call.Items[0].Content[0].Text != "hello" {
		t.Fatalf("items=%#v", call.Items)
	}
}
