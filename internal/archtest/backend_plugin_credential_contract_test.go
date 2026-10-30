package archtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	inframanifest "github.com/aiproxer/aiproxer/internal/infra/backendplugins/manifest"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	sdkmanifest "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
)

// manifestTemplateWithContract is the census projection extended with the
// optional per-export v2 credential contract. Templates carry placeholder
// digest/build fields and per-platform executables that the strict install
// parser validates, so this gate decodes leniently and enforces the contract
// rule itself: every static export must carry a valid, admitted registration
// contract. The parser's own strictness is covered by the manifest package
// tests.
type manifestTemplateWithContract struct {
	Schema   string `json:"schema"`
	PluginID string `json:"plugin_id"`
	Exports  []struct {
		Kind               string                            `json:"kind"`
		CredentialMode     string                            `json:"credential_mode"`
		AccessScope        string                            `json:"access_scope"`
		ProcessSharing     string                            `json:"process_sharing"`
		CredentialContract *backendplugin.CredentialContract `json:"credential_contract"`
	} `json:"exports"`
}

// TestBackendPluginManifestStaticExportsDeclareCredentialContract proves
// requirement 5.1 at the architecture gate: every static export of every
// first-party connector template carries an admitted registration contract.
// A new API-key connector without its contract fails this gate.
func TestBackendPluginManifestStaticExportsDeclareCredentialContract(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(firstPartyConnectorsDir)))
	if err != nil {
		t.Fatalf("read connectors dir: %v", err)
	}
	var checked, static int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		tmpl := filepath.Join(root, filepath.FromSlash(firstPartyConnectorsDir), e.Name(), "manifest", "template.backendplugin.json")
		raw, err := os.ReadFile(tmpl)
		if err != nil {
			continue
		}
		var decoded manifestTemplateWithContract
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("%s: %v", tmpl, err)
		}
		if len(decoded.Exports) == 0 {
			continue
		}
		checked++
		for _, templateExport := range decoded.Exports {
			if templateExport.CredentialMode != "static" {
				continue
			}
			static++
			exp := sdkmanifest.Export{
				Kind:               templateExport.Kind,
				CredentialMode:     backendplugin.CredentialMode(templateExport.CredentialMode),
				AccessScope:        backendplugin.AccessScope(templateExport.AccessScope),
				ProcessSharing:     backendplugin.ProcessSharing(templateExport.ProcessSharing),
				CredentialContract: templateExport.CredentialContract,
			}
			if templateExport.CredentialContract == nil {
				t.Fatalf("%s: static export %q declares no credential contract", tmpl, exp.Kind)
			}
			if err := exp.CredentialContract.Validate(); err != nil {
				t.Fatalf("%s: static export %q has no valid credential contract: %v", tmpl, exp.Kind, err)
			}
			if err := inframanifest.AdmitStaticExport(exp); err != nil {
				t.Fatalf("%s: static export %q not admitted: %v", tmpl, exp.Kind, err)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no connector templates checked; gate must never be silently empty")
	}
	if static == 0 {
		t.Fatal("no static exports checked; gate must cover the static cohort")
	}
	t.Logf("checked %d templates covering %d static exports", checked, static)
}

// customCompatibleCredentialKinds lists the in-process compatible factory
// kinds whose credential namespace is bound per instance (requirement 1.4).
// The IDs come from the standardplugins-owned constants, never literals.
func customCompatibleCredentialKinds() []string {
	return []string{
		standardplugins.CustomOpenAILegacyCompatibleID,
		standardplugins.CustomOpenAIResponsesCompatibleID,
		standardplugins.CustomAnthropicCompatibleID,
		standardplugins.CustomOpenResponsesCompatibleID,
		standardplugins.CustomSystemOneCompatibleID,
	}
}

// TestBackendPluginCredentialNamespaceCoversOperatorStaticApprovals binds the
// namespace registry to the host-owned multi-user approval table: every
// operator-static approval must resolve to a registered credential namespace
// (fixed registration or known custom-root kind), so the registry cannot
// drift behind the security profiles it protects.
func TestBackendPluginCredentialNamespaceCoversOperatorStaticApprovals(t *testing.T) {
	t.Parallel()
	custom := customCompatibleCredentialKinds()
	for _, approval := range standardplugins.HostMultiUserBackendPolicy().Approvals() {
		if approval.CredentialClass != standardplugins.MultiUserCredentialOperatorStatic {
			continue
		}
		kind := approval.FactoryKind
		if inframanifest.IsRegisteredCredentialKind(kind) {
			continue
		}
		known := slices.Contains(custom, kind)
		if !known {
			t.Errorf("operator-static approval %q has no registered credential namespace", kind)
		}
	}
}
