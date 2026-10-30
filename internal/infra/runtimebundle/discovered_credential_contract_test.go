package runtimebundle_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/catalog"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/discovery"
	inframanifest "github.com/aiproxer/aiproxer/internal/infra/backendplugins/manifest"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	sdkmanifest "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
)

func staticExportDescriptor(kind string, contract *backendplugin.CredentialContract) discovery.Descriptor {
	return discovery.Descriptor{
		SafeID: "r/" + kind + ".json",
		Status: discovery.StatusDiscovered,
		Manifest: sdkmanifest.Manifest{
			PluginID: "com.aiproxer." + kind, ProtocolMajor: 1, ProtocolMinMinor: 0, ProtocolMaxMinor: 1,
			Exports: []sdkmanifest.Export{{
				Kind: kind, CredentialMode: backendplugin.CredentialModeStatic,
				AccessScope: backendplugin.AccessScopeAny, ProcessSharing: backendplugin.ProcessSharingPerInstance,
				CredentialContract: contract,
			}},
		},
	}
}

func collectOne(t *testing.T, d discovery.Descriptor) ([]runtimebundle.ValidatedExport, error) {
	t.Helper()
	art := &trust.VerifiedArtifact{DigestHex: "credential-contract-digest"}
	snap, err := catalog.Resolve(catalog.Input{
		Discovered:  []discovery.Descriptor{d},
		TrustBySafe: map[string]trust.VerifyResult{d.SafeID: {Reason: trust.ReasonOK, Artifact: art}},
		HostMajor:   1, HostMinor: 0,
	})
	if err != nil {
		t.Fatalf("catalog resolve: %v", err)
	}
	return runtimebundle.CollectInstallableExports(snap, map[string]trust.VerifyResult{
		d.SafeID: {Reason: trust.ReasonOK, Artifact: art},
	}, []discovery.Descriptor{d})
}

// TestDiscovered_StaticExportWithoutContractRejected proves the host security
// profile gate fails closed on unclassified static-key exports: a static
// manifest export with no credential contract must not become installable.
func TestDiscovered_StaticExportWithoutContractRejected(t *testing.T) {
	t.Parallel()
	d := staticExportDescriptor("openrouter", nil)
	if _, err := collectOne(t, d); err == nil {
		t.Fatal("static export without credential contract must be rejected")
	}
}

// TestDiscovered_UnregisteredStaticKindRejected proves each new API-key
// connector is rejected without its registration contract, even when it
// carries a well-formed manifest block.
func TestDiscovered_UnregisteredStaticKindRejected(t *testing.T) {
	t.Parallel()
	contract, ok := inframanifest.LookupCanonicalContract("openrouter")
	if !ok {
		t.Skip("registry unavailable; namespace tests cover registration")
	}
	d := staticExportDescriptor("future-acme-inference", &contract)
	if _, err := collectOne(t, d); err == nil {
		t.Fatal("unregistered static kind must be rejected")
	}
}

// TestDiscovered_RegisteredStaticExportAdmitted proves a registered static
// export with its canonical contract stays installable.
func TestDiscovered_RegisteredStaticExportAdmitted(t *testing.T) {
	t.Parallel()
	contract, ok := inframanifest.LookupCanonicalContract("openrouter")
	if !ok {
		t.Fatal("openrouter must be registered")
	}
	d := staticExportDescriptor("openrouter", &contract)
	exports, err := collectOne(t, d)
	if err != nil {
		t.Fatalf("registered static export must be admitted: %v", err)
	}
	if len(exports) != 1 || exports[0].Kind != "openrouter" {
		t.Fatalf("exports=%+v", exports)
	}
}

// TestDiscovered_AdmittedExportCarriesContractAndManifest proves the
// activation inputs for task 4's provisioning lane: the validated export
// owns its admitted credential contract (namespace identity for host - root
// resolution, independent of the discovery snapshot lifetime) and the
// packaged manifest the activation-time descriptor comparison checks
// against. A non-static export carries neither.
func TestDiscovered_AdmittedExportCarriesContractAndManifest(t *testing.T) {
	t.Parallel()
	contract, ok := inframanifest.LookupCanonicalContract("openrouter")
	if !ok {
		t.Fatal("openrouter must be registered")
	}
	d := staticExportDescriptor("openrouter", &contract)
	exports, err := collectOne(t, d)
	if err != nil {
		t.Fatal(err)
	}
	got := exports[0].Contract
	if got == nil || *got != contract {
		t.Fatalf("export contract = %+v, want the admitted canonical contract", got)
	}
	if len(exports[0].Manifest.Exports) != 1 || exports[0].Manifest.Exports[0].Kind != "openrouter" {
		t.Fatalf("export manifest = %+v, want the packaged openrouter manifest", exports[0].Manifest)
	}
}

// TestDiscovered_NonStaticExportWithoutContractAdmitted guards v1
// compatibility: non-static exports still install without any contract.
func TestDiscovered_NonStaticExportWithoutContractAdmitted(t *testing.T) {
	t.Parallel()
	kind := "dynamic-nostatic-kind"
	d := discovery.Descriptor{
		SafeID: "r/nostatic.json",
		Status: discovery.StatusDiscovered,
		Manifest: sdkmanifest.Manifest{
			PluginID: "com.aiproxer.nostatic", ProtocolMajor: 1, ProtocolMinMinor: 0, ProtocolMaxMinor: 1,
			Exports: []sdkmanifest.Export{{
				Kind: kind, CredentialMode: backendplugin.CredentialModeNone,
				AccessScope: backendplugin.AccessScopeLocalOnly, ProcessSharing: backendplugin.ProcessSharingPerInstance,
			}},
		},
	}
	exports, err := collectOne(t, d)
	if err != nil {
		t.Fatalf("non-static export must stay installable: %v", err)
	}
	if len(exports) != 1 {
		t.Fatalf("exports=%+v", exports)
	}
}
