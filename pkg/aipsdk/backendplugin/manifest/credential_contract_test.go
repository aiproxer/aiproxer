package manifest_test

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
)

func contractBase(exec aipsdk.BackendExecutionClass) manifest.Manifest {
	return manifest.Manifest{
		Schema: manifest.SchemaV1, PluginID: "io.x.y", Version: "1", BuildID: "b",
		Executable: "bin/x", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ProtocolMajor: 1, Platforms: []manifest.Platform{{OS: "linux", Arch: "amd64"}},
		Exports: []manifest.Export{{
			Kind: "k", CredentialMode: backendplugin.CredentialModeStatic,
			AccessScope: backendplugin.AccessScopeAny, ProcessSharing: backendplugin.ProcessSharingPerInstance,
			ExecutionClass: exec,
		}},
	}
}

func staticContract() *backendplugin.CredentialContract {
	return &backendplugin.CredentialContract{
		Version:     backendplugin.CredentialContractVersion,
		Class:       backendplugin.CredentialClassAPIKey,
		Source:      backendplugin.CredentialSourceHostEnv,
		EnvRoot:     "EXAMPLE_API_KEY",
		Slot:        backendplugin.CredentialSlotAPIKey,
		Selection:   backendplugin.CredentialSelectionQuotaPreOutput,
		QuotaDomain: "example",
	}
}

// TestManifestValidate_StaticExportWithoutContractRejected is the fail-closed
// gate for unclassified static-key exports (requirements 1.3, 5.1): a static
// export with no credential contract must not validate.
func TestManifestValidate_StaticExportWithoutContractRejected(t *testing.T) {
	t.Parallel()
	err := contractBase(aipsdk.BackendExecutionInference).Validate()
	if err == nil {
		t.Fatal("static export without credential contract must be rejected")
	}
	if !errors.Is(err, manifest.ErrMissingCredentialContract) {
		t.Fatalf("want %v, got %v", manifest.ErrMissingCredentialContract, err)
	}
	if !errors.Is(err, manifest.ErrInvalidManifest) {
		t.Fatalf("missing contract must still classify as invalid manifest, got %v", err)
	}
}

func TestManifestValidate_StaticExportWithContractAccepted(t *testing.T) {
	t.Parallel()
	m := contractBase(aipsdk.BackendExecutionInference)
	m.Exports[0].CredentialContract = staticContract()
	if err := m.Validate(); err != nil {
		t.Fatalf("static export with valid contract rejected: %v", err)
	}
}

func TestManifestValidate_NonStaticExportMustNotCarryContract(t *testing.T) {
	t.Parallel()
	for _, mode := range []backendplugin.CredentialMode{
		backendplugin.CredentialModeNone,
		backendplugin.CredentialModeWorkload,
		backendplugin.CredentialModeOAuthUser,
		backendplugin.CredentialModeUnknown,
	} {
		m := contractBase(aipsdk.BackendExecutionInference)
		m.Exports[0].CredentialMode = mode
		m.Exports[0].AccessScope = backendplugin.AccessScopeLocalOnly
		m.Exports[0].CredentialContract = staticContract()
		if err := m.Validate(); err == nil {
			t.Fatalf("non-static mode %q must not carry a credential contract", mode)
		}
	}
}

func TestManifestValidate_NonStaticV1Compatibility(t *testing.T) {
	t.Parallel()
	m := contractBase(aipsdk.BackendExecutionInference)
	m.Exports[0].CredentialMode = backendplugin.CredentialModeNone
	m.Exports[0].AccessScope = backendplugin.AccessScopeLocalOnly
	if err := m.Validate(); err != nil {
		t.Fatalf("v1 non-static export without contract must stay valid, got: %v", err)
	}
}

func TestManifestValidate_StaticExportWithBadContractRejected(t *testing.T) {
	t.Parallel()
	m := contractBase(aipsdk.BackendExecutionInference)
	bad := staticContract()
	bad.Source = "file"
	m.Exports[0].CredentialContract = bad
	if err := m.Validate(); err == nil {
		t.Fatal("static export with invalid contract must be rejected")
	}
}
