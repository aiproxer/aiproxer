package aipsdk_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestValidateRegistrations_allowsTwoBackendsSameFactoryDifferentInstance(t *testing.T) {
	t.Parallel()

	mandatory := []aipsdk.Requirement{{Kind: aipsdk.PluginKindBackend, ID: "openai-responses"}}
	err := aipsdk.ValidateRegistrations([]aipsdk.Registration{
		{ID: "openai-primary", FactoryKind: "openai-responses", Kind: aipsdk.PluginKindBackend, Enabled: true},
		{ID: "openai-fallback", FactoryKind: "openai-responses", Kind: aipsdk.PluginKindBackend, Enabled: true},
	}, mandatory)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}
