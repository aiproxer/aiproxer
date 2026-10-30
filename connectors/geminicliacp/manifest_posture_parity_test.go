package geminicliacp_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/geminicliacp/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// manifestExport is the security-relevant projection of one packaged manifest
// export. The packaged manifest is the host's trusted security-policy authority,
// so the runtime descriptor must never disagree with it.
type manifestExport struct {
	Kind           string `json:"kind"`
	CredentialMode string `json:"credential_mode"`
	AccessScope    string `json:"access_scope"`
	ProcessSharing string `json:"process_sharing"`
	ExecutionClass string `json:"execution_class"`
}

func packagedManifestExports(t *testing.T) map[string]manifestExport {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(wd, "manifest", "template.backendplugin.json"))
	if err != nil {
		t.Fatalf("packaged manifest template: %v", err)
	}
	var doc struct {
		PluginID string           `json:"plugin_id"`
		Exports  []manifestExport `json:"exports"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	d, err := service.New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.PluginID != doc.PluginID {
		t.Fatalf("manifest plugin_id %q != runtime plugin_id %q", doc.PluginID, d.PluginID)
	}
	out := make(map[string]manifestExport, len(doc.Exports))
	for _, ex := range doc.Exports {
		out[ex.Kind] = ex
	}
	return out
}

// TestParity_PackagedManifestMatchesRuntimeDescriptor keeps the packaged manifest
// and the runtime FactoryDescriptor in agreement, and pins the local-only posture
// of this connector family in both places.
//
// This is a consistency and diagnostic guard only. The multi-user decision is made
// once by the host at the composition boundary, never inside Describe or Configure,
// and this connector declares no local single-user check of its own.
func TestParity_PackagedManifestMatchesRuntimeDescriptor(t *testing.T) {
	t.Parallel()
	d, err := service.New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	declared := packagedManifestExports(t)
	if len(declared) != len(d.Factories) {
		t.Fatalf("manifest declares %d exports, Describe reports %d: %+v", len(declared), len(d.Factories), d.Factories)
	}
	for _, fac := range d.Factories {
		want, ok := declared[fac.Kind]
		if !ok {
			t.Errorf("runtime factory %q is not declared by manifest/template.backendplugin.json", fac.Kind)
			continue
		}
		if string(fac.CredentialMode) != want.CredentialMode {
			t.Errorf("factory %q: descriptor credential_mode %q != manifest %q", fac.Kind, fac.CredentialMode, want.CredentialMode)
		}
		if string(fac.AccessScope) != want.AccessScope {
			t.Errorf("factory %q: descriptor access_scope %q != manifest %q", fac.Kind, fac.AccessScope, want.AccessScope)
		}
		if string(fac.ProcessSharing) != want.ProcessSharing {
			t.Errorf("factory %q: descriptor process_sharing %q != manifest %q", fac.Kind, fac.ProcessSharing, want.ProcessSharing)
		}
		if want.AccessScope != "local_only" {
			t.Errorf("factory %q: manifest access_scope %q; a backend bound to one operator's local trust boundary must stay local_only", fac.Kind, want.AccessScope)
		}
		if got := wantExecutionClass(fac.Kind); got == "" {
			t.Errorf("factory %q is not a known export of this connector; declare its expected execution class here", fac.Kind)
		} else if want.ExecutionClass != got {
			t.Errorf("factory %q: manifest execution_class %q, want %q", fac.Kind, want.ExecutionClass, got)
		}
		if err := backendplugin.ValidateCredentialMode(fac.CredentialMode); err != nil {
			t.Errorf("factory %q: descriptor credential_mode %q is not a known posture", fac.Kind, fac.CredentialMode)
		}
	}
}

// // wantExecutionClass pins the reviewed execution class of every export this
// connector publishes. Returning "" for an unknown kind fails the parity test
// closed, so a new export cannot be added to the manifest without an explicit
// classification decision here.
func wantExecutionClass(kind string) string {
	switch kind {
	case "geminicliacp":
		return "agent_runtime"
	default:
		return ""
	}
}
