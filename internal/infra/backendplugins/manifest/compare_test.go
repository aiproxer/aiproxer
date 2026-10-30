package manifest_test

import (
	"testing"

	inframanifest "github.com/aiproxer/aiproxer/internal/infra/backendplugins/manifest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	sdkmanifest "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
)

func compareManifest() sdkmanifest.Manifest {
	return sdkmanifest.Manifest{
		Schema: sdkmanifest.SchemaV1, PluginID: "com.aiproxer.example", Version: "1", BuildID: "b",
		Executable: "bin/example", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ProtocolMajor: 1, Platforms: []sdkmanifest.Platform{{OS: "linux", Arch: "amd64"}},
		Exports: []sdkmanifest.Export{{
			Kind: "example-a", CredentialMode: backendplugin.CredentialModeStatic,
			AccessScope: backendplugin.AccessScopeAny, ProcessSharing: backendplugin.ProcessSharingPerInstance,
			CredentialContract: registeredContract("EXAMPLE_A_API_KEY", "example-a"),
		}, {
			Kind: "example-b", CredentialMode: backendplugin.CredentialModeNone,
			AccessScope: backendplugin.AccessScopeLocalOnly, ProcessSharing: backendplugin.ProcessSharingPerInstance,
		}},
	}
}

func compareDescriptor() backendplugin.PluginDescriptor {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1, PluginID: "com.aiproxer.example", Version: "1",
		Factories: []backendplugin.FactoryDescriptor{{
			Kind: "example-a", CredentialMode: backendplugin.CredentialModeStatic,
			AccessScope: backendplugin.AccessScopeAny, ProcessSharing: backendplugin.ProcessSharingPerInstance,
		}, {
			Kind: "example-b", CredentialMode: backendplugin.CredentialModeNone,
			AccessScope: backendplugin.AccessScopeLocalOnly, ProcessSharing: backendplugin.ProcessSharingPerInstance,
		}},
	}
}

// TestCompareDescriptorToManifest_AgreeingPairPasses proves the validated
// descriptor comparison accepts a runtime descriptor matching its manifest.
func TestCompareDescriptorToManifest_AgreeingPairPasses(t *testing.T) {
	t.Parallel()
	if err := inframanifest.CompareDescriptorToManifest(compareManifest(), compareDescriptor()); err != nil {
		t.Fatalf("agreeing pair rejected: %v", err)
	}
}

func TestCompareDescriptorToManifest_MissingExportRejected(t *testing.T) {
	t.Parallel()
	desc := compareDescriptor()
	desc.Factories = desc.Factories[:1]
	if err := inframanifest.CompareDescriptorToManifest(compareManifest(), desc); err == nil {
		t.Fatal("descriptor missing a manifest export must be rejected")
	}
}

func TestCompareDescriptorToManifest_ExtraExportRejected(t *testing.T) {
	t.Parallel()
	desc := compareDescriptor()
	desc.Factories = append(desc.Factories, backendplugin.FactoryDescriptor{
		Kind: "example-sneaky", CredentialMode: backendplugin.CredentialModeNone,
		AccessScope: backendplugin.AccessScopeLocalOnly, ProcessSharing: backendplugin.ProcessSharingPerInstance,
	})
	if err := inframanifest.CompareDescriptorToManifest(compareManifest(), desc); err == nil {
		t.Fatal("descriptor with an extra export must be rejected")
	}
}

func TestCompareDescriptorToManifest_PostureMismatchRejected(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		mutate func(*backendplugin.FactoryDescriptor)
	}{
		{"credential mode", func(f *backendplugin.FactoryDescriptor) {
			f.CredentialMode = backendplugin.CredentialModeNone
		}},
		{"access scope", func(f *backendplugin.FactoryDescriptor) {
			f.AccessScope = backendplugin.AccessScopeLocalOnly
		}},
		{"process sharing", func(f *backendplugin.FactoryDescriptor) {
			f.ProcessSharing = backendplugin.ProcessSharingSharedArtifact
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			desc := compareDescriptor()
			tc.mutate(&desc.Factories[0])
			if err := inframanifest.CompareDescriptorToManifest(compareManifest(), desc); err == nil {
				t.Fatalf("%s mismatch must be rejected", tc.name)
			}
		})
	}
}

// TestCompareDescriptorToManifest_StaticDescriptorRequiresManifestContract
// proves G14 at the comparison layer: a static runtime factory with no
// manifest contract (the old ollama-cloud contradiction) fails closed.
func TestCompareDescriptorToManifest_StaticDescriptorRequiresManifestContract(t *testing.T) {
	t.Parallel()
	m := compareManifest()
	m.Exports[0].CredentialContract = nil
	if err := inframanifest.CompareDescriptorToManifest(m, compareDescriptor()); err == nil {
		t.Fatal("static descriptor without manifest contract must be rejected")
	}
}
