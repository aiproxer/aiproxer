package catalog_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/catalog"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/discovery"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	sdkmanifest "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
)

func TestCatalog_DuplicatePluginAndExportConflict(t *testing.T) {
	t.Parallel()
	d1 := discovery.Descriptor{
		SafeID: "r/a.json", Status: discovery.StatusDiscovered,
		Manifest: sdkmanifest.Manifest{
			PluginID: "io.dup", ProtocolMajor: 1, ProtocolMinMinor: 0, ProtocolMaxMinor: 1,
			Exports: []sdkmanifest.Export{{Kind: "shared"}},
		},
	}
	d2 := discovery.Descriptor{
		SafeID: "r/b.json", Status: discovery.StatusDiscovered,
		Manifest: sdkmanifest.Manifest{
			PluginID: "io.dup", ProtocolMajor: 1, ProtocolMinMinor: 0, ProtocolMaxMinor: 1,
			Exports: []sdkmanifest.Export{{Kind: "other"}},
		},
	}
	snap, err := catalog.Resolve(catalog.Input{
		Discovered: []discovery.Descriptor{d1, d2},
		TrustBySafe: map[string]trust.VerifyResult{
			"r/a.json": {Reason: trust.ReasonOK, Artifact: &trust.VerifiedArtifact{}},
			"r/b.json": {Reason: trust.ReasonOK, Artifact: &trust.VerifiedArtifact{}},
		},
		HostMajor: 1, HostMinor: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range snap.Entries {
		if e.Reason == catalog.ReasonDuplicatePluginID && len(e.Owners) == 2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("%+v", snap.Entries)
	}
}

func TestCatalog_BuiltinCollisionAndEnabledFatal(t *testing.T) {
	t.Parallel()
	d := discovery.Descriptor{
		SafeID: "r/c.json", Status: discovery.StatusDiscovered,
		Manifest: sdkmanifest.Manifest{
			PluginID: "io.c", ProtocolMajor: 1, ProtocolMinMinor: 0, ProtocolMaxMinor: 1,
			Exports: []sdkmanifest.Export{{Kind: "openai-responses"}},
		},
	}
	_, err := catalog.Resolve(catalog.Input{
		Discovered:   []discovery.Descriptor{d},
		TrustBySafe:  map[string]trust.VerifyResult{"r/c.json": {Reason: trust.ReasonOK, Artifact: &trust.VerifiedArtifact{}}},
		BuiltinKinds: []string{"openai-responses"},
		EnabledKinds: []string{"openai-responses"},
		HostMajor:    1, HostMinor: 0,
	})
	if err == nil {
		t.Fatal("expected enabled unresolved")
	}
}

func TestCatalog_InvalidUnusedNonFatal(t *testing.T) {
	t.Parallel()
	d := discovery.Descriptor{SafeID: "r/bad.json", Status: discovery.StatusInvalid, Reason: "parse_failed"}
	snap, err := catalog.Resolve(catalog.Input{
		Discovered: []discovery.Descriptor{d},
		HostMajor:  1, HostMinor: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Entries) != 1 || snap.Entries[0].State != catalog.StateInvalid {
		t.Fatalf("%+v", snap.Entries)
	}
}

func TestCatalog_StrictInvalidFatal(t *testing.T) {
	t.Parallel()
	d := discovery.Descriptor{SafeID: "r/bad.json", Status: discovery.StatusInvalid}
	_, err := catalog.Resolve(catalog.Input{
		Discovered: []discovery.Descriptor{d},
		Strict:     true,
		HostMajor:  1, HostMinor: 0,
	})
	if err == nil {
		t.Fatal("expected strict error")
	}
}

func TestCatalog_MissingTrustFailClosed(t *testing.T) {
	t.Parallel()
	d := discovery.Descriptor{
		SafeID: "r/u.json", Status: discovery.StatusDiscovered,
		Manifest: sdkmanifest.Manifest{
			PluginID: "io.u", ProtocolMajor: 1, ProtocolMinMinor: 0, ProtocolMaxMinor: 1,
			Exports: []sdkmanifest.Export{{Kind: "u"}},
		},
	}
	snap, err := catalog.Resolve(catalog.Input{
		Discovered: []discovery.Descriptor{d},
		HostMajor:  1, HostMinor: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Entries) != 1 || snap.Entries[0].State != catalog.StateUntrusted {
		t.Fatalf("%+v", snap.Entries)
	}
}

func TestCatalog_ProtocolIncompatible(t *testing.T) {
	t.Parallel()
	d := discovery.Descriptor{
		SafeID: "r/p.json", Status: discovery.StatusDiscovered,
		Manifest: sdkmanifest.Manifest{
			PluginID: "io.p", ProtocolMajor: 1, ProtocolMinMinor: 2, ProtocolMaxMinor: 3,
			Exports: []sdkmanifest.Export{{Kind: "p"}},
		},
	}
	snap, err := catalog.Resolve(catalog.Input{
		Discovered:  []discovery.Descriptor{d},
		TrustBySafe: map[string]trust.VerifyResult{"r/p.json": {Reason: trust.ReasonOK, Artifact: &trust.VerifiedArtifact{}}},
		HostMajor:   1, HostMinor: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Entries[0].State != catalog.StateIncompatible {
		t.Fatalf("%+v", snap.Entries[0])
	}
}

func TestCatalog_OwnersDeepCopied(t *testing.T) {
	t.Parallel()
	d1 := discovery.Descriptor{
		SafeID: "r/a.json", Status: discovery.StatusDiscovered,
		Manifest: sdkmanifest.Manifest{
			PluginID: "io.dup", ProtocolMajor: 1, ProtocolMinMinor: 0, ProtocolMaxMinor: 1,
			Exports: []sdkmanifest.Export{{Kind: "shared"}},
		},
	}
	d2 := discovery.Descriptor{
		SafeID: "r/b.json", Status: discovery.StatusDiscovered,
		Manifest: sdkmanifest.Manifest{
			PluginID: "io.dup", ProtocolMajor: 1, ProtocolMinMinor: 0, ProtocolMaxMinor: 1,
			Exports: []sdkmanifest.Export{{Kind: "other"}},
		},
	}
	snap, err := catalog.Resolve(catalog.Input{
		Discovered: []discovery.Descriptor{d1, d2},
		TrustBySafe: map[string]trust.VerifyResult{
			"r/a.json": {Reason: trust.ReasonOK, Artifact: &trust.VerifiedArtifact{}},
			"r/b.json": {Reason: trust.ReasonOK, Artifact: &trust.VerifiedArtifact{}},
		},
		HostMajor: 1, HostMinor: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	var owners []string
	for _, e := range snap.Entries {
		if e.Reason == catalog.ReasonDuplicatePluginID {
			owners = e.Owners
			break
		}
	}
	if len(owners) < 2 {
		t.Fatalf("%+v", snap.Entries)
	}
	owners[0] = "mutated"
	snap2, err := catalog.Resolve(catalog.Input{
		Discovered: []discovery.Descriptor{d1, d2},
		TrustBySafe: map[string]trust.VerifyResult{
			"r/a.json": {Reason: trust.ReasonOK, Artifact: &trust.VerifiedArtifact{}},
			"r/b.json": {Reason: trust.ReasonOK, Artifact: &trust.VerifiedArtifact{}},
		},
		HostMajor: 1, HostMinor: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range snap2.Entries {
		if e.Reason == catalog.ReasonDuplicatePluginID && e.Owners[0] == "mutated" {
			t.Fatal("owners slice aliased across snapshots")
		}
	}
}
