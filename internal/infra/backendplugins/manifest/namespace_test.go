package manifest_test

import (
	"testing"

	inframanifest "github.com/aiproxer/aiproxer/internal/infra/backendplugins/manifest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	sdkmanifest "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
)

func registeredContract(root, domain string) *backendplugin.CredentialContract {
	return &backendplugin.CredentialContract{
		Version:     backendplugin.CredentialContractVersion,
		Class:       backendplugin.CredentialClassAPIKey,
		Source:      backendplugin.CredentialSourceHostEnv,
		EnvRoot:     root,
		Slot:        backendplugin.CredentialSlotAPIKey,
		Selection:   backendplugin.CredentialSelectionQuotaPreOutput,
		QuotaDomain: domain,
	}
}

// TestNamespaceRegistry_CanonicalRootsAreUniqueOrExplicitAlias proves requirement
// 1.3: accidental root collisions fail, expressly declared same-provider sharing
// (OpenAI Chat/Responses on OPENAI_API_KEY) passes.
func TestNamespaceRegistry_CanonicalRootsAreUniqueOrExplicitAlias(t *testing.T) {
	t.Parallel()
	if err := inframanifest.ValidateCredentialNamespaceRegistry(); err != nil {
		t.Fatalf("canonical registry inconsistent: %v", err)
	}
	openAIChat, ok := inframanifest.LookupCanonicalContract("openai-legacy")
	if !ok {
		t.Fatal("built-in openai-legacy must be registered")
	}
	openAIResponses, ok := inframanifest.LookupCanonicalContract("openai-responses")
	if !ok {
		t.Fatal("built-in openai-responses must be registered")
	}
	if openAIChat.EnvRoot != "OPENAI_API_KEY" || openAIResponses.EnvRoot != "OPENAI_API_KEY" {
		t.Fatalf("OpenAI adapters must share OPENAI_API_KEY, got %q and %q", openAIChat.EnvRoot, openAIResponses.EnvRoot)
	}
	if !inframanifest.IsDeclaredSameProviderAlias("openai-legacy", "openai-responses") {
		t.Fatal("OpenAI Chat/Responses sharing must be an explicit declared alias")
	}
	if inframanifest.IsDeclaredSameProviderAlias("openai-legacy", "anthropic") {
		t.Fatal("unrelated providers must never be declared aliases")
	}
}

// TestNamespaceRegistry_CommandCodeRootsAreSeparate proves G10: the two
// CommandCode exports must not share one generic root.
func TestNamespaceRegistry_CommandCodeRootsAreSeparate(t *testing.T) {
	t.Parallel()
	openAI, ok := inframanifest.LookupCanonicalContract("commandcode-openai")
	if !ok {
		t.Fatal("commandcode-openai must be registered")
	}
	anthropic, ok := inframanifest.LookupCanonicalContract("commandcode-anthropic")
	if !ok {
		t.Fatal("commandcode-anthropic must be registered")
	}
	if openAI.EnvRoot == anthropic.EnvRoot {
		t.Fatalf("CommandCode exports must use separate roots, both use %q", openAI.EnvRoot)
	}
}

// TestNamespaceRegistry_OllamaCloudEnumerated proves every Ollama Cloud export
// is registered with a static posture (G14).
func TestNamespaceRegistry_OllamaCloudEnumerated(t *testing.T) {
	t.Parallel()
	cloud, ok := inframanifest.LookupCanonicalContract("ollama-cloud")
	if !ok {
		t.Fatal("ollama-cloud must be registered")
	}
	if cloud.EnvRoot != "OLLAMA_CLOUD_API_KEY" {
		t.Fatalf("ollama-cloud root = %q, want OLLAMA_CLOUD_API_KEY", cloud.EnvRoot)
	}
}

// TestNamespaceRegistry_UnregisteredStaticKindRejected proves 5.1/5.3: a new
// API-key connector without a registration contract is rejected at admission.
func TestNamespaceRegistry_UnregisteredStaticKindRejected(t *testing.T) {
	t.Parallel()
	future := sdkmanifest.Export{
		Kind: "future-acme-inference", CredentialMode: backendplugin.CredentialModeStatic,
		AccessScope: backendplugin.AccessScopeAny, ProcessSharing: backendplugin.ProcessSharingPerInstance,
		CredentialContract: registeredContract("ACME_API_KEY", "acme"),
	}
	if err := inframanifest.AdmitStaticExport(future); err == nil {
		t.Fatal("unregistered static kind must be rejected even with a well-formed contract")
	}
}

// TestNamespaceRegistry_ManifestContractMustMatchRegistration proves the
// manifest-authored binding cannot silently diverge from the host registry.
func TestNamespaceRegistry_ManifestContractMustMatchRegistration(t *testing.T) {
	t.Parallel()
	known, ok := inframanifest.LookupCanonicalContract("openrouter")
	if !ok {
		t.Fatal("openrouter must be registered")
	}
	drifted := sdkmanifest.Export{
		Kind: "openrouter", CredentialMode: backendplugin.CredentialModeStatic,
		AccessScope: backendplugin.AccessScopeAny, ProcessSharing: backendplugin.ProcessSharingPerInstance,
		CredentialContract: registeredContract("ROGUE_API_KEY", known.QuotaDomain),
	}
	if err := inframanifest.AdmitStaticExport(drifted); err == nil {
		t.Fatal("manifest contract diverging from registration must be rejected")
	}
	aligned := sdkmanifest.Export{
		Kind: "openrouter", CredentialMode: backendplugin.CredentialModeStatic,
		AccessScope: backendplugin.AccessScopeAny, ProcessSharing: backendplugin.ProcessSharingPerInstance,
		CredentialContract: &known,
	}
	if err := inframanifest.AdmitStaticExport(aligned); err != nil {
		t.Fatalf("registered manifest contract must be admitted: %v", err)
	}
}

// TestNamespaceRegistry_StaticWithoutContractRejected proves fail-closed
// admission for unclassified static-key exports.
func TestNamespaceRegistry_StaticWithoutContractRejected(t *testing.T) {
	t.Parallel()
	bare := sdkmanifest.Export{
		Kind: "openrouter", CredentialMode: backendplugin.CredentialModeStatic,
		AccessScope: backendplugin.AccessScopeAny, ProcessSharing: backendplugin.ProcessSharingPerInstance,
	}
	if err := inframanifest.AdmitStaticExport(bare); err == nil {
		t.Fatal("static export without contract must be rejected at admission")
	}
}
