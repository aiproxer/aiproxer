package claudeagentacp_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/claudeagentacp/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// manifestExport projects the host's security-policy authority; the runtime
// descriptor must never disagree with the packaged manifest.
type manifestExport struct {
	Kind           string `json:"kind"`
	CredentialMode string `json:"credential_mode"`
	AccessScope    string `json:"access_scope"`
	ProcessSharing string `json:"process_sharing"`
	ExecutionClass string `json:"execution_class"`
}

type packagedManifest struct {
	PluginID         string           `json:"plugin_id"`
	ProtocolMajor    uint32           `json:"protocol_major"`
	ProtocolMinMinor uint32           `json:"protocol_min_minor"`
	ProtocolMaxMinor uint32           `json:"protocol_max_minor"`
	Exports          []manifestExport `json:"exports"`
}

func packagedManifestDocument(t *testing.T) packagedManifest {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(wd, "manifest", "template.backendplugin.json"))
	if err != nil {
		t.Fatalf("packaged manifest template: %v", err)
	}
	var doc packagedManifest
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// This is parity only; the host owns the actual multi-user rejection decision.
func TestParity_PackagedManifestMatchesRuntimeDescriptor(t *testing.T) {
	t.Parallel()
	d, err := service.New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	doc := packagedManifestDocument(t)
	if d.PluginID != doc.PluginID {
		t.Fatalf("manifest plugin_id %q != runtime plugin_id %q", doc.PluginID, d.PluginID)
	}
	if d.ProtocolMajor != doc.ProtocolMajor || d.ProtocolMinor < doc.ProtocolMinMinor || d.ProtocolMinor > doc.ProtocolMaxMinor {
		t.Fatalf("runtime protocol %d.%d is outside packaged manifest range %d.[%d,%d]",
			d.ProtocolMajor, d.ProtocolMinor, doc.ProtocolMajor, doc.ProtocolMinMinor, doc.ProtocolMaxMinor)
	}
	declared := make(map[string]manifestExport, len(doc.Exports))
	for _, ex := range doc.Exports {
		declared[ex.Kind] = ex
	}
	if len(declared) != len(d.Factories) {
		t.Fatalf("manifest declares %d exports, Describe reports %d", len(declared), len(d.Factories))
	}
	for _, fac := range d.Factories {
		want, ok := declared[fac.Kind]
		if !ok {
			t.Errorf("runtime factory %q is not declared by manifest", fac.Kind)
			continue
		}
		if string(fac.CredentialMode) != want.CredentialMode || want.CredentialMode != "static" {
			t.Errorf("factory %q: credential_mode descriptor=%q manifest=%q; want static", fac.Kind, fac.CredentialMode, want.CredentialMode)
		}
		if string(fac.AccessScope) != want.AccessScope || want.AccessScope != "local_only" {
			t.Errorf("factory %q: access_scope descriptor=%q manifest=%q; want local_only", fac.Kind, fac.AccessScope, want.AccessScope)
		}
		if string(fac.ProcessSharing) != want.ProcessSharing || want.ProcessSharing != "per_instance" {
			t.Errorf("factory %q: process_sharing descriptor=%q manifest=%q; want per_instance", fac.Kind, fac.ProcessSharing, want.ProcessSharing)
		}
		if fac.Kind != "claudeagentacp" || want.ExecutionClass != "agent_runtime" {
			t.Errorf("unexpected agent export: %+v", want)
		}
		if err := backendplugin.ValidateCredentialMode(fac.CredentialMode); err != nil {
			t.Error(err)
		}
	}
}
