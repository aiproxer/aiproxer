package aipapi_test

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestDeriveProtocolRequirements_ignoresFrontendWireMetadataExtensions(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Extensions: map[string]json.RawMessage{
			"openairesponses.model":      json.RawMessage(`"gpt-4o-mini"`),
			"openrouter.upstream_flavor": json.RawMessage(`"responses"`),
		},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	req := aipapi.DeriveProtocolRequirements(call)
	for _, c := range req.Capabilities {
		if c == aipapi.CapabilityOpaqueExtensions {
			t.Fatalf("wire metadata must not require opaque extensions: %#v", req)
		}
	}
	if len(req.ExtensionTypes) != 0 {
		t.Fatalf("extension types=%#v", req.ExtensionTypes)
	}
}
