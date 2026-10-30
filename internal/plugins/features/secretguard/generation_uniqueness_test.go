package secretguard_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestEnabledRegistrations_GenerationFeatureUniquenessRejectsDuplicates(t *testing.T) {
	t.Parallel()
	_, err := secretguard.EnabledRegistrations([]aipsdk.Registration{
		{Kind: aipsdk.PluginKindFeature, ID: "sg-a", FactoryKind: "secrets-guard", Enabled: true},
		{Kind: aipsdk.PluginKindFeature, ID: "sg-b", FactoryKind: "secrets-guard", Enabled: true},
	})
	if err == nil {
		t.Fatal("expected uniqueness violation")
	}
	if !strings.Contains(err.Error(), "secrets-guard") {
		t.Fatalf("err=%v", err)
	}
}

func TestComposeRuntimeConfig_GenerationFeatureUniquenessBeforeDecode(t *testing.T) {
	t.Parallel()
	_, err := secretguard.ComposeRuntimeConfig("single_user", []aipsdk.Registration{
		{Kind: aipsdk.PluginKindFeature, ID: "sg-a", FactoryKind: "secrets-guard", Enabled: true},
		{Kind: aipsdk.PluginKindFeature, ID: "sg-b", FactoryKind: "secrets-guard", Enabled: true},
	})
	if err == nil {
		t.Fatal("expected uniqueness violation")
	}
}
